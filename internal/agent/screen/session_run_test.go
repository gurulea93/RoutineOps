//go:build screen

package screen

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

// blockingStream — стрим, у которого Recv ждёт сообщения (in) или закрытия, в отличие от
// fakeScreenStream, где пустая очередь сразу даёт EOF и рвёт loopDown.
type blockingStream struct {
	*fakeScreenStream
	in   chan *pb.SessionDown
	done chan struct{}
	once sync.Once
}

func newBlockingStream() *blockingStream {
	return &blockingStream{fakeScreenStream: &fakeScreenStream{}, in: make(chan *pb.SessionDown), done: make(chan struct{})}
}

func (b *blockingStream) Recv() (*pb.SessionDown, error) {
	select {
	case m := <-b.in:
		return m, nil
	case <-b.done:
		return nil, io.EOF
	}
}
func (b *blockingStream) CloseSend() error { return nil }
func (b *blockingStream) stop()            { b.once.Do(func() { close(b.done) }) }

// launchWith — «процесс» захвата на io.Pipe: script пишет в канал worker → служба;
// stdin службы читается и выбрасывается до закрытия (тогда «процесс» выходит).
func launchWith(script func(out io.Writer)) func(wireConfig) (*workerProc, EndReason, error) {
	return func(wireConfig) (*workerProc, EndReason, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, inR); _ = outW.Close(); close(done) }()
		go script(outW)
		return &workerProc{
			stdin: inW, stdout: outR,
			wait: func() error { <-done; return nil },
			kill: func() { _ = outW.Close(); _ = inW.Close() },
		}, "", nil
	}
}

func helloOnly(out io.Writer) {
	_ = writeJSONMsg(out, msgHello, wireHello{Width: 1024, Height: 768, Profile: "low"})
}

func dialWith(bs *blockingStream) func(*session, wireHello) (*sessionStream, EndReason, error) {
	return func(s *session, _ wireHello) (*sessionStream, EndReason, error) {
		if !s.activate() {
			return nil, ReasonProtocolError, errors.New("not requested")
		}
		return &sessionStream{AgentService_ScreenSessionClient: bs, close: func() { bs.stop(); s.cancel() }}, "", nil
	}
}

func testDeps(launch func(wireConfig) (*workerProc, EndReason, error), bs *blockingStream) (*runDeps, *fakeSink) {
	sink := &fakeSink{log: &callLog{}}
	d := &runDeps{launch: launch, sink: sink, log: quietLog(), now: fixedNow}
	if bs != nil {
		d.dial = dialWith(bs)
	} else {
		d.dial = func(*session, wireHello) (*sessionStream, EndReason, error) {
			panic("dial must not be called")
		}
	}
	return d, sink
}

func runToEnd(t *testing.T, d *runDeps, cmd *pb.ScreenSessionCommand, during func()) *session {
	t.Helper()
	m := NewManager(context.Background(), d.run, quietLog())
	s, r := m.Start(cmd)
	if s == nil {
		t.Fatalf("not started: %q", r)
	}
	done := make(chan struct{})
	go func() { m.Wait(); close(done) }()
	if during != nil {
		during()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not finish")
	}
	return s
}

func eventReason(t *testing.T, sink *fakeSink) string {
	t.Helper()
	var ev pb.ScreenEvent
	if err := proto.Unmarshal(sink.data, &ev); err != nil || ev.Kind != "end" {
		t.Fatalf("terminal event: %+v err %v", &ev, err)
	}
	return ev.Reason
}

func TestRunWorkerEndsSession(t *testing.T) {
	bs := newBlockingStream()
	d, sink := testDeps(launchWith(func(out io.Writer) {
		helloOnly(out)
		_ = writeJSONMsg(out, msgEnd, wireEnd{Reason: "CAPTURE_FAILED", Detail: "x"})
	}), bs)
	s := runToEnd(t, d, cmdID("s1"), nil)

	if s.reason() != ReasonCaptureFailed || eventReason(t, sink) != "CAPTURE_FAILED" {
		t.Fatalf("reason %q event %q", s.reason(), eventReason(t, sink))
	}
	msgs := bs.messages()
	if len(msgs) == 0 || msgs[len(msgs)-1].GetEnd().GetReason() != "CAPTURE_FAILED" {
		t.Fatalf("stream did not get SessionEnd: %v", msgs)
	}
}

func TestRunServerStop(t *testing.T) {
	bs := newBlockingStream()
	d, sink := testDeps(launchWith(helloOnly), bs)
	s := runToEnd(t, d, cmdID("s1"), func() { bs.in <- stopMsg("VIEWER_GONE") })

	if s.reason() != ReasonViewerGone || eventReason(t, sink) != "VIEWER_GONE" {
		t.Fatalf("reason %q", s.reason())
	}
}

func TestRunMaxDuration(t *testing.T) {
	bs := newBlockingStream()
	d, sink := testDeps(launchWith(helloOnly), bs)
	d.ceiling = 50 * time.Millisecond
	s := runToEnd(t, d, cmdID("s1"), nil)

	if s.reason() != ReasonMaxDuration || eventReason(t, sink) != "MAX_DURATION" {
		t.Fatalf("reason %q", s.reason())
	}
}

func TestRunRefusesInputBeforeLaunch(t *testing.T) {
	launched := false
	d, sink := testDeps(func(wireConfig) (*workerProc, EndReason, error) { launched = true; return nil, "", nil }, nil)
	s := runToEnd(t, d, &pb.ScreenSessionCommand{SessionId: "s1", AllowInput: true}, nil)

	if launched || s.reason() != ReasonInputUnavailable || eventReason(t, sink) != "INPUT_UNAVAILABLE" {
		t.Fatalf("launched %v reason %q", launched, s.reason())
	}
}

func TestRunLaunchRefusalKeepsWorkerReason(t *testing.T) {
	d, sink := testDeps(func(wireConfig) (*workerProc, EndReason, error) {
		return nil, ReasonNoConsoleUser, errors.New("no user")
	}, nil)
	s := runToEnd(t, d, cmdID("s1"), nil)

	if s.reason() != ReasonNoConsoleUser || eventReason(t, sink) != "NO_CONSOLE_USER" {
		t.Fatalf("reason %q", s.reason())
	}
}

func TestRunHelloTimeout(t *testing.T) {
	d, _ := testDeps(launchWith(func(io.Writer) {}), nil)
	d.hello = 30 * time.Millisecond
	s := runToEnd(t, d, cmdID("s1"), nil)

	if s.reason() != ReasonWorkerLost {
		t.Fatalf("reason %q", s.reason())
	}
}

func TestRunFirstMessageNotHello(t *testing.T) {
	d, _ := testDeps(launchWith(func(out io.Writer) { _ = writeJSONMsg(out, msgStats, wireStats{}) }), nil)
	s := runToEnd(t, d, cmdID("s1"), nil)

	if s.reason() != ReasonProtocolError {
		t.Fatalf("reason %q", s.reason())
	}
}

func TestServiceStartSession(t *testing.T) {
	bs := newBlockingStream()
	d, _ := testDeps(launchWith(helloOnly), bs)
	svc := NewService(context.Background(), d, quietLog())
	ctx := context.Background()

	if code, err := svc.StartSession(ctx, "t1", cmdID("s1")); code != "" || err != nil {
		t.Fatalf("accepted session: %q %v", code, err)
	}
	if code, err := svc.StartSession(ctx, "t2", cmdID("s2")); code != "SESSION_BUSY" || err == nil {
		t.Fatalf("busy: %q %v", code, err)
	}
	bs.in <- stopMsg("VIEWER_GONE")
	svc.mgr.Wait()
}

func TestServiceStartSessionRefusal(t *testing.T) {
	d, _ := testDeps(func(wireConfig) (*workerProc, EndReason, error) { panic("must not launch") }, nil)
	svc := NewService(context.Background(), d, quietLog())

	code, err := svc.StartSession(context.Background(), "t1", &pb.ScreenSessionCommand{SessionId: "s1", AllowInput: true})
	if code != "INPUT_UNAVAILABLE" || err == nil {
		t.Fatalf("refusal: %q %v", code, err)
	}
	svc.mgr.Wait()
}
