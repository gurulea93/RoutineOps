//go:build screen

package screen

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

type readHarness struct {
	t    *testing.T
	s    *session
	box  *latestFrame
	w    *io.PipeWriter
	done chan EndReason
}

// startRead запускает readPipe на io.Pipe: тест пишет в w так, как писал бы захватчик.
func startRead(t *testing.T, ctx context.Context) *readHarness {
	t.Helper()
	r, w := io.Pipe()
	h := &readHarness{t: t, s: newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"}),
		box: newLatestFrame(nil), w: w, done: make(chan EndReason, 1)}
	go func() { h.done <- readPipe(ctx, h.s, r, h.box, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() { _ = w.Close() })
	return h
}

func (h *readHarness) send(f *upFrame) {
	h.t.Helper()
	for _, p := range f.parts {
		if err := writeFrameMsg(h.w, p); err != nil {
			h.t.Fatal(err)
		}
	}
}

// next ждёт кадр в box (readPipe работает в своей горутине).
func (h *readHarness) next() *upFrame {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f := h.box.take(); f != nil {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("no frame reached the box")
	return nil
}

func (h *readHarness) result() EndReason {
	h.t.Helper()
	select {
	case r := <-h.done:
		return r
	case <-time.After(2 * time.Second):
		h.t.Fatal("readPipe did not return")
		return ""
	}
}

func TestReadPipeFramesInOrder(t *testing.T) {
	h := startRead(t, context.Background())
	h.send(gridFrame(t, 1, true, 300, 200, 1, gridAll...))
	if f := h.next(); f.seq != 1 || !f.key {
		t.Fatalf("first frame %d key %v", f.seq, f.key)
	}
	// Незнакомое сообщение пропускается и сеанс не роняет.
	if err := writeMsg(h.w, 99, []byte("future")); err != nil {
		t.Fatal(err)
	}
	big := splitFrame(t, 2, false, 6, 100<<10) // несколько частей
	h.send(&upFrame{seq: 2, parts: big})
	if f := h.next(); f.seq != 2 || len(f.parts) != len(big) {
		t.Fatalf("multi-part frame: seq %d parts %d", f.seq, len(f.parts))
	}
	// seq не растёт — кадр не уходит и считается ошибкой.
	h.send(gridFrame(t, 2, false, 300, 200, 3, tilePos{0, 0}))
	h.send(gridFrame(t, 3, false, 300, 200, 3, tilePos{0, 0}))
	if f := h.next(); f.seq != 3 {
		t.Fatalf("seq %d after rejected repeat", f.seq)
	}
	if _, errs := h.box.counters(); errs != 1 {
		t.Fatalf("errors %d", errs)
	}
	if err := writeJSONMsg(h.w, msgEnd, wireEnd{Reason: "VIEWER_GONE", Detail: "stop requested"}); err != nil {
		t.Fatal(err)
	}
	if r := h.result(); r != "VIEWER_GONE" {
		t.Fatalf("end reason %q", r)
	}
	if _, _, d := h.s.snapshot(); d != "stop requested" {
		t.Fatalf("worker detail not kept: %q", d)
	}
}

func TestReadPipeUnexpectedEOFIsWorkerLost(t *testing.T) {
	h := startRead(t, context.Background())
	h.send(gridFrame(t, 1, true, 300, 200, 1, gridAll...))
	h.next()
	_ = h.w.Close()
	if r := h.result(); r != ReasonWorkerLost {
		t.Fatalf("reason %q", r)
	}
	if st, _, detail := h.s.snapshot(); st != StateEnded || h.s.endReason != ReasonWorkerLost || detail == "" {
		t.Fatalf("session %v %q detail %q", st, h.s.endReason, detail)
	}
}

func TestReadPipeContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := startRead(t, ctx)
	cancel() // пайп молчит: Read висит, но readPipe обязан вернуться
	if r := h.result(); r != "" {
		t.Fatalf("reason %q", r)
	}
	if st, _, _ := h.s.snapshot(); st == StateEnded {
		t.Fatal("cancel must not end the session from readPipe")
	}
}
