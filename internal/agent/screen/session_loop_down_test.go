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
