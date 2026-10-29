//go:build screen

package screen

import (
	"context"
	"io"
	"log/slog"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

func newTestManager(run func(*session)) *Manager {
	return NewManager(context.Background(), run, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func cmdID(id string) *pb.ScreenSessionCommand { return &pb.ScreenSessionCommand{SessionId: id} }

func TestManagerSecondSessionIsBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	m := newTestManager(func(s *session) { close(started); <-release })

	s1, r := m.Start(cmdID("s1"))
	if s1 == nil || r != "" {
		t.Fatalf("first: %v %q", s1, r)
	}
	<-started
	if s2, r := m.Start(cmdID("s2")); s2 != nil || r != ReasonSessionBusy {
		t.Fatalf("second: %v %q", s2, r)
	}
	if !m.Busy() {
		t.Fatal("must be busy while the first runs")
	}
	close(release)
	m.Wait()
}

func TestManagerSlotFreedAfterFinish(t *testing.T) {
	m := newTestManager(func(s *session) {})
	if s, r := m.Start(cmdID("s1")); s == nil || r != "" {
		t.Fatalf("first: %q", r)
	}
	m.Wait()
	if m.Busy() {
		t.Fatal("slot not released")
	}
	if s, r := m.Start(cmdID("s2")); s == nil || r != "" {
		t.Fatalf("after finish: %q", r)
	}
	m.Wait()
}

func TestManagerPanicFreesSlotAndEndsSession(t *testing.T) {
	m := newTestManager(func(s *session) { panic("boom") })
	s, r := m.Start(cmdID("s1"))
	if s == nil || r != "" {
		t.Fatalf("first: %q", r)
	}
	m.Wait()

	st, _, detail := s.snapshot()
	if st != StateEnded || s.endReason != ReasonWorkerLost || detail != "panic: boom" {
		t.Fatalf("state %v reason %q detail %q", st, s.endReason, detail)
	}
	if m.Busy() {
		t.Fatal("slot not released after panic")
	}
	if s2, r := m.Start(cmdID("s2")); s2 == nil || r != "" {
		t.Fatalf("after panic: %q", r)
	}
	m.Wait()
}

func TestManagerParentCancelCancelsSession(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	running, done := make(chan *session, 1), make(chan struct{})
	m := NewManager(parent, func(s *session) { running <- s; <-s.ctx.Done(); close(done) },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start(cmdID("s1"))
	<-running
	cancel()
	<-done
	m.Wait()
}
