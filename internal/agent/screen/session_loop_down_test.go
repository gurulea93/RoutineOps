//go:build screen

package screen

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

func runLoopDown(t *testing.T, ctx context.Context, s *session, fake *fakeScreenStream) {
	t.Helper()
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	done := make(chan struct{})
	go func() {
		loopDown(ctx, s, st, &ctlWriter{w: io.Discard}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loopDown did not exit")
	}
}

func TestLoopDownEOFEndsSessionTransportLost(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	fake.push(&pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{RequestKeyframe: true}}})
	fake.push(&pb.SessionDown{Msg: &pb.SessionDown_Accepted{Accepted: &pb.SessionAccepted{}}})
	runLoopDown(t, context.Background(), s, fake)

	st, _, detail := s.snapshot()
	if st != StateEnded || s.endReason != ReasonTransportLost || detail != io.EOF.Error() {
		t.Fatalf("state %v reason %q detail %q", st, s.endReason, detail)
	}
	if len(fake.recvQueue) != 0 {
		t.Fatalf("%d messages left unread", len(fake.recvQueue))
	}
}

func TestLoopDownCancelledContextDoesNotEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	runLoopDown(t, ctx, s, &fakeScreenStream{}) // Recv сразу io.EOF

	if st, _, _ := s.snapshot(); st == StateEnded {
		t.Fatal("loopDown ended the session on a cancelled context")
	}
}

func stopMsg(reason string) *pb.SessionDown {
	return &pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{
		Stop: true, StopReason: reason, RequestKeyframe: true, // stop важнее
	}}}
}

func TestLoopDownStopReasons(t *testing.T) {
	long := strings.Repeat("ж", 300) // 2 байта на символ: обрезка обязана быть по символам
	cases := []struct {
		name, raw, detail string
		reason            EndReason
	}{
		{"known", "VIEWER_GONE", "", ReasonViewerGone},
		{"unknown long", long, strings.Repeat("ж", 256), ReasonOperatorRevoked},
		{"empty", "", "", ReasonOperatorRevoked},
	}
	for _, c := range cases {
		s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
		fake := &fakeScreenStream{}
		fake.push(stopMsg(c.raw))
		fake.push(&pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{RequestKeyframe: true}}})
		var ctlBuf bytes.Buffer
		runLoopDownCtl(t, s, fake, &ctlBuf)

		st, _, detail := s.snapshot()
		if st != StateEnded || s.endReason != c.reason || detail != c.detail {
			t.Fatalf("%s: state %v reason %q detail %d runes", c.name, st, s.endReason, len([]rune(detail)))
		}
		if ctlBuf.Len() != 0 {
			t.Fatalf("%s: keyframe requested although stop has priority", c.name)
		}
		if len(fake.recvQueue) != 1 {
			t.Fatalf("%s: loop must exit right after stop", c.name)
		}
	}
}

func TestLoopDownRequestKeyframeWritesToWorker(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	fake.push(&pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{RequestKeyframe: true}}})
	var ctlBuf bytes.Buffer
	runLoopDownCtl(t, s, fake, &ctlBuf) // затем io.EOF → TRANSPORT_LOST

	typ, body, err := readMsg(&ctlBuf)
	if err != nil || typ != msgKeyframe || len(body) != 0 || ctlBuf.Len() != 0 {
		t.Fatalf("worker got type %d body %d err %v, %d bytes left", typ, len(body), err, ctlBuf.Len())
	}
}

func runLoopDownCtl(t *testing.T, s *session, fake *fakeScreenStream, w io.Writer) {
	t.Helper()
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	done := make(chan struct{})
	go func() {
		loopDown(context.Background(), s, st, &ctlWriter{w: w}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loopDown did not exit")
	}
}

// runLoopDownLog — как runLoopDownCtl, но лог пишется в буфер (уровень Debug), чтобы
// проверять предупреждения. Буфер читать только после возврата: к тому времени цикл вышел.
func runLoopDownLog(t *testing.T, s *session, fake *fakeScreenStream, w io.Writer) string {
	t.Helper()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	done := make(chan struct{})
	go func() {
		loopDown(context.Background(), s, st, &ctlWriter{w: w}, log)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loopDown did not exit")
	}
	return logBuf.String()
}

func narrowMsg(displays []int32, allowInput bool) *pb.SessionDown {
	return &pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{
		Narrow: &pb.ScreenScope{Displays: displays, AllowInput: allowInput},
	}}}
}

func keyframeMsg() *pb.SessionDown {
	return &pb.SessionDown{Msg: &pb.SessionDown_Control{Control: &pb.SessionControl{RequestKeyframe: true}}}
}

func TestCheckNarrow(t *testing.T) {
	cases := []struct {
		name string
		cmd  *pb.ScreenSessionCommand
		req  *pb.ScreenScope
		want narrowVerdict
	}{
		{"primary only, explicit primary", &pb.ScreenSessionCommand{}, &pb.ScreenScope{Displays: []int32{0}}, narrowOK},
		{"primary only, extra display", &pb.ScreenSessionCommand{}, &pb.ScreenScope{Displays: []int32{0, 1}}, narrowExpand},
		{"primary only, other display", &pb.ScreenSessionCommand{}, &pb.ScreenScope{Displays: []int32{1}}, narrowExpand},
		{"two displays, subset", &pb.ScreenSessionCommand{Displays: []int32{0, 1}}, &pb.ScreenScope{Displays: []int32{1}}, narrowOK},
		{"two displays, same", &pb.ScreenSessionCommand{Displays: []int32{0, 1}}, &pb.ScreenScope{Displays: []int32{0, 1}}, narrowOK},
		{"two displays, third", &pb.ScreenSessionCommand{Displays: []int32{0, 1}}, &pb.ScreenScope{Displays: []int32{2}}, narrowExpand},
		{"empty displays", &pb.ScreenSessionCommand{}, &pb.ScreenScope{}, narrowEmpty},
		{"input granted, kept", &pb.ScreenSessionCommand{AllowInput: true}, &pb.ScreenScope{Displays: []int32{0}, AllowInput: true}, narrowOK},
		{"input granted, dropped", &pb.ScreenSessionCommand{AllowInput: true}, &pb.ScreenScope{Displays: []int32{0}}, narrowOK},
		{"input not granted, requested", &pb.ScreenSessionCommand{}, &pb.ScreenScope{Displays: []int32{0}, AllowInput: true}, narrowExpand},
		{"expansion wins over empty", &pb.ScreenSessionCommand{}, &pb.ScreenScope{AllowInput: true}, narrowExpand},
	}
	for _, c := range cases {
		if got := checkNarrow(c.cmd, c.req); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestLoopDownNarrowValidContinues(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	fake.push(narrowMsg([]int32{0}, false))
	fake.push(keyframeMsg())
	var ctlBuf bytes.Buffer
	runLoopDownLog(t, s, fake, &ctlBuf)

	if typ, _, err := readMsg(&ctlBuf); err != nil || typ != msgKeyframe {
		t.Fatalf("keyframe after narrow not written: type %d err %v", typ, err)
	}
	if st, _, _ := s.snapshot(); st != StateEnded || s.endReason != ReasonTransportLost {
		t.Fatalf("state %v reason %q", st, s.endReason)
	}
}

func TestLoopDownNarrowExpansionIgnored(t *testing.T) {
	for name, msg := range map[string]*pb.SessionDown{
		"extra display": narrowMsg([]int32{0, 1}, false),
		"allow_input":   narrowMsg([]int32{0}, true),
	} {
		s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
		fake := &fakeScreenStream{}
		fake.push(msg)
		fake.push(keyframeMsg())
		var ctlBuf bytes.Buffer
		logs := runLoopDownLog(t, s, fake, &ctlBuf)

		if !strings.Contains(logs, "expansion attempt rejected") {
			t.Fatalf("%s: expansion not logged: %s", name, logs)
		}
		if typ, _, err := readMsg(&ctlBuf); err != nil || typ != msgKeyframe {
			t.Fatalf("%s: loop must continue after rejected expansion", name)
		}
		if s.endReason != ReasonTransportLost {
			t.Fatalf("%s: session ended with %q, want only EOF", name, s.endReason)
		}
	}
}

func TestLoopDownNarrowEmptyEndsSession(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	fake.push(narrowMsg(nil, false))
	fake.push(keyframeMsg())
	var ctlBuf bytes.Buffer
	runLoopDownLog(t, s, fake, &ctlBuf)

	st, _, detail := s.snapshot()
	if st != StateEnded || s.endReason != ReasonOperatorRevoked || detail != "narrow left no displays" {
		t.Fatalf("state %v reason %q detail %q", st, s.endReason, detail)
	}
	if ctlBuf.Len() != 0 || len(fake.recvQueue) != 1 {
		t.Fatalf("loop must exit right after empty narrow: %d ctl bytes, %d unread", ctlBuf.Len(), len(fake.recvQueue))
	}
}

func TestLoopDownSessionInputWarnsOnce(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	for i := 0; i < 3; i++ {
		fake.push(&pb.SessionDown{Msg: &pb.SessionDown_Input{Input: &pb.SessionInput{}}})
	}
	fake.push(keyframeMsg())
	var ctlBuf bytes.Buffer
	logs := runLoopDownLog(t, s, fake, &ctlBuf)

	if n := strings.Count(logs, "SessionInput ignored"); n != 1 {
		t.Fatalf("warned %d times, want 1: %s", n, logs)
	}
	if typ, _, err := readMsg(&ctlBuf); err != nil || typ != msgKeyframe {
		t.Fatal("loop must continue after SessionInput")
	}
}
