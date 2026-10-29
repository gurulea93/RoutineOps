//go:build screen

package screen

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Floodww/RoutineOps/internal/agent/outbox"
	pb "github.com/Floodww/RoutineOps/proto"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// callLog — общий журнал порядка вызовов между очередью, захватчиком и стримом.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(s string) { c.mu.Lock(); c.calls = append(c.calls, s); c.mu.Unlock() }
func (c *callLog) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

type fakeSink struct {
	log  *callLog
	kind string
	data []byte
	err  error
}

func (f *fakeSink) Enqueue(kind string, data []byte) error {
	f.log.add("enqueue")
	f.kind, f.data = kind, data
	return f.err
}

func newFakeWorker(log *callLog) *workerProc {
	return &workerProc{
		stdin: nopWriteCloser{io.Discard},
		wait:  func() error { log.add("worker-exit"); return nil },
		kill:  func() { log.add("worker-kill") },
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fixedNow() time.Time { return time.UnixMilli(1_700_000_000_123) }

func TestFinishSessionOrderAndEvent(t *testing.T) {
	cl := &callLog{}
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	s.end(ReasonViewerGone, strings.Repeat("ж", 300))
	s.setStats(wireStats{Frames: 5, Bytes: 99, Stalls: 1, Dropped: 2})
	fake := &fakeScreenStream{}
	st := &sessionStream{AgentService_ScreenSessionClient: nil, close: func() { cl.add("stream-close") }}
	st.AgentService_ScreenSessionClient = &closeSendStream{fakeScreenStream: fake, log: cl}
	sink := &fakeSink{log: cl}

	finishSession(s, st, newFakeWorker(cl), sink, quietLog(), fixedNow)

	got := cl.get()
	if len(got) < 3 || got[0] != "enqueue" || got[1] != "worker-exit" || got[len(got)-1] != "stream-close" {
		t.Fatalf("order %v", got)
	}
	if sink.kind != outbox.KindScreenSession {
		t.Fatalf("kind %q", sink.kind)
	}
	var ev pb.ScreenEvent
	if err := proto.Unmarshal(sink.data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.SessionId != "s1" || ev.Kind != "end" || ev.Reason != "VIEWER_GONE" || ev.AtMs != 1_700_000_000_123 ||
		len([]rune(ev.Detail)) != maxStopDetail {
		t.Fatalf("event %+v", &ev)
	}
	msgs := fake.messages()
	if len(msgs) != 2 || msgs[0].GetStats().GetFrames() != 5 || msgs[1].GetEnd().GetReason() != "VIEWER_GONE" ||
		len([]rune(msgs[1].GetEnd().GetDetail())) != maxStopDetail {
		t.Fatalf("stream messages %v", msgs)
	}
}

// closeSendStream дописывает CloseSend к fakeScreenStream (у того ClientStream nil).
type closeSendStream struct {
	*fakeScreenStream
	log *callLog
}

func (c *closeSendStream) CloseSend() error { c.log.add("close-send"); return nil }

func TestFinishSessionQueueFailureStillCloses(t *testing.T) {
	cl := &callLog{}
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	s.end(ReasonCaptureFailed, "x")
	sink := &fakeSink{log: cl, err: errors.New("disk full")}
	finishSession(s, nil, newFakeWorker(cl), sink, quietLog(), fixedNow)

	if got := cl.get(); len(got) != 2 || got[1] != "worker-exit" {
		t.Fatalf("worker must still be closed: %v", got)
	}
}

func TestFinishSessionNotEndedGetsTransportLost(t *testing.T) {
	cl := &callLog{}
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	sink := &fakeSink{log: cl}
	finishSession(s, nil, nil, sink, quietLog(), fixedNow)

	var ev pb.ScreenEvent
	if err := proto.Unmarshal(sink.data, &ev); err != nil || ev.Reason != "TRANSPORT_LOST" {
		t.Fatalf("event %+v err %v", &ev, err)
	}
}

// hangStream: Send висит, пока стрим не закроют.
type hangStream struct {
	*fakeScreenStream
	release chan struct{}
}

func (h *hangStream) Send(*pb.SessionUp) error { <-h.release; return errors.New("cancelled") }
func (h *hangStream) CloseSend() error         { return nil }

func TestFinishSessionHangingStreamDoesNotBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("waits endSendTimeout")
	}
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	s.end(ReasonStopped, "")
	h := &hangStream{fakeScreenStream: &fakeScreenStream{}, release: make(chan struct{})}
	closed := false
	st := &sessionStream{AgentService_ScreenSessionClient: h, close: func() { closed = true; close(h.release) }}

	done := make(chan struct{})
	go func() { finishSession(s, st, nil, &fakeSink{log: &callLog{}}, quietLog(), fixedNow); close(done) }()
	select {
	case <-done:
	case <-time.After(endSendTimeout + 2*time.Second):
		t.Fatal("finishSession blocked on a hanging stream")
	}
	if !closed {
		t.Fatal("stream not closed")
	}
}
