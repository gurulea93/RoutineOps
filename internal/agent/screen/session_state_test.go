//go:build screen

package screen

import (
	"context"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestSessionTransitions(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	if st, _, d := s.snapshot(); st != StateRequested || d != "" {
		t.Fatalf("new session %v %q", st, d)
	}
	if !s.activate() || s.activate() {
		t.Fatal("activate must succeed exactly once")
	}
	r, closed := s.end(ReasonTransportLost, "send hello: EOF")
	if r != ReasonTransportLost || !closed || s.ctx.Err() == nil {
		t.Fatalf("end %q closed %v ctx %v", r, closed, s.ctx.Err())
	}
	if s.activate() {
		t.Fatal("ended session re-activated")
	}
}

func TestSessionFirstEndWins(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	s.end(ReasonWorkerLost, "EOF")
	r, closed := s.end(ReasonTransportLost, "stream broken")
	if r != ReasonWorkerLost || closed {
		t.Fatalf("second end: %q closed %v", r, closed)
	}
	if st, _, d := s.snapshot(); st != StateEnded || s.endReason != ReasonWorkerLost || d != "EOF" {
		t.Fatalf("state %v reason %q detail %q", st, s.endReason, d)
	}
}
