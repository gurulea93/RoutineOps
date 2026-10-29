//go:build screen

package screen

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestSleepCtx(t *testing.T) {
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	mustNotTimer := func(time.Duration) <-chan time.Time { t.Fatal("timer created for d <= 0"); return nil }

	if !sleepCtx(context.Background(), 0, mustNotTimer) || !sleepCtx(context.Background(), -time.Second, mustNotTimer) {
		t.Fatal("zero/negative duration must return true at once")
	}

	fired := func(time.Duration) <-chan time.Time {
		c := make(chan time.Time)
		close(c)
		return c
	}
	if !sleepCtx(context.Background(), time.Hour, fired) {
		t.Fatal("elapsed timer must return true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if sleepCtx(ctx, time.Hour, never) {
		t.Fatal("cancelled context must return false")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled sleep waited")
	}

	// d <= 0 не смотрит на ctx вовсе: ждать нечего.
	if !sleepCtx(ctx, 0, mustNotTimer) {
		t.Fatal("zero duration with cancelled context must return true")
	}
}

func partsFrame(seq uint32, key bool, sizes ...int) upFrame {
	f := upFrame{seq: seq, key: key}
	for i, n := range sizes {
		f.parts = append(f.parts, wireFrame{Seq: seq, Keyframe: key, Last: i == len(sizes)-1,
			CapturedMs: int64(1000 + i), Payload: make([]byte, n)})
	}
	return f
}

func TestSendPartsSingle(t *testing.T) {
	var got []*pb.SessionFrame
	n, err := sendParts(func(m *pb.SessionUp) error { got = append(got, m.GetFrame()); return nil }, partsFrame(7, true, 40))
	if err != nil || n != 40 || len(got) != 1 {
		t.Fatalf("n %d err %v sent %d", n, err, len(got))
	}
	if f := got[0]; f.Seq != 7 || !f.Keyframe || !f.LastPart || len(f.Payload) != 40 || f.CapturedAtMs != 1000 {
		t.Fatalf("frame %+v", f)
	}
}

func TestSendPartsMulti(t *testing.T) {
	var got []*pb.SessionFrame
	n, err := sendParts(func(m *pb.SessionUp) error { got = append(got, m.GetFrame()); return nil }, partsFrame(9, false, 10, 20, 30))
	if err != nil || n != 60 || len(got) != 3 {
		t.Fatalf("n %d err %v sent %d", n, err, len(got))
	}
	for i, f := range got {
		if f.Seq != 9 || f.Keyframe || f.LastPart != (i == 2) || len(f.Payload) != 10*(i+1) || f.CapturedAtMs != int64(1000+i) {
			t.Fatalf("part %d: %+v", i, f)
		}
	}
}

func TestSendPartsStopsOnError(t *testing.T) {
	boom := errors.New("stream broken")
	calls := 0
	n, err := sendParts(func(*pb.SessionUp) error {
		calls++
		if calls == 2 {
			return boom
		}
		return nil
	}, partsFrame(1, false, 10, 20, 30))
	if !errors.Is(err, boom) || n != 10 || calls != 2 {
		t.Fatalf("n %d err %v calls %d", n, err, calls)
	}
}

func TestSendPartsEmpty(t *testing.T) {
	n, err := sendParts(func(*pb.SessionUp) error { t.Fatal("send called for an empty frame"); return nil }, upFrame{seq: 1})
	if n != 0 || err != nil {
		t.Fatalf("n %d err %v", n, err)
	}
}

func TestSendLoopSendErrorEndsSession(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	box := newLatestFrame(nil)
	box.put(gridFrame(t, 1, true, 300, 200, 1, gridAll...))
	fake := &fakeScreenStream{failAt: 1, failErr: errors.New("stream reset by peer")}
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	budget := newBandwidthBudget(ProfileLow, (&fakeClock{t: time.Unix(1, 0)}).now)
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }

	done := make(chan struct{})
	go func() {
		sendLoop(s.ctx, s, box, budget, st, never)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendLoop did not exit after a send error")
	}
	st0, _, detail := s.snapshot()
	if st0 != StateEnded || s.endReason != ReasonTransportLost || !strings.Contains(detail, "stream reset by peer") {
		t.Fatalf("state %v reason %q detail %q", st0, s.endReason, detail)
	}
	if len(fake.messages()) != 0 {
		t.Fatal("failed send must not be recorded")
	}
}

func TestSendLoopSendsWholeFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newSession(ctx, &pb.ScreenSessionCommand{SessionId: "s1"})
	parts := splitFrame(t, 5, true, 6, 100<<10) // ~600 KiB → несколько частей
	want := 0
	for _, p := range parts {
		want += len(p.Payload)
	}
	box := newLatestFrame(nil)
	box.put(&upFrame{seq: 5, key: true, parts: parts})
	fake := &fakeScreenStream{}
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	budget := newBandwidthBudget(ProfileLow, (&fakeClock{t: time.Unix(1, 0)}).now)

	// after зовётся после отправки и setStats: кадр больше секунды бюджета, пауза > 0.
	paused := make(chan struct{}, 1)
	fired := func(time.Duration) <-chan time.Time {
		paused <- struct{}{}
		c := make(chan time.Time)
		close(c)
		return c
	}
	done := make(chan struct{})
	go func() {
		sendLoop(s.ctx, s, box, budget, st, fired)
		close(done)
	}()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("frame was not sent")
	}

	msgs := fake.messages()
	if len(msgs) != len(parts) {
		t.Fatalf("sent %d parts, want %d", len(msgs), len(parts))
	}
	for i, m := range msgs {
		f := m.GetFrame()
		if f == nil || f.Seq != 5 || !f.Keyframe || f.LastPart != (i == len(parts)-1) || !bytes.Equal(f.Payload, parts[i].Payload) {
			t.Fatalf("part %d out of order or wrong: %+v", i, f)
		}
	}
	if _, stats, _ := s.snapshot(); stats.Frames != 1 || stats.Bytes != uint64(want) {
		t.Fatalf("stats %+v, want 1 frame / %d bytes", stats, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendLoop did not exit after cancel")
	}
}

func TestSendLoopCancelWhileWaitingForFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	fake := &fakeScreenStream{}
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	budget := newBandwidthBudget(ProfileLow, (&fakeClock{t: time.Unix(1, 0)}).now)
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }

	done := make(chan struct{})
	go func() {
		sendLoop(ctx, s, newLatestFrame(nil), budget, st, never)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendLoop did not exit after cancel")
	}
	if n := len(fake.messages()); n != 0 {
		t.Fatalf("stream received %d messages", n)
	}
	if st0, _, _ := s.snapshot(); st0 == StateEnded {
		t.Fatal("sendLoop must not end the session on cancel")
	}
}

func TestSendLoopCancelDuringBudgetPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1"})
	parts := splitFrame(t, 1, true, 6, 100<<10) // ~600 KiB: больше секунды бюджета, пауза > 0
	box := newLatestFrame(nil)
	box.put(&upFrame{seq: 1, key: true, parts: parts})
	fake := &fakeScreenStream{}
	st := &sessionStream{AgentService_ScreenSessionClient: fake, close: func() {}}
	budget := newBandwidthBudget(ProfileLow, (&fakeClock{t: time.Unix(1, 0)}).now)

	// Пауза, которая не кончается сама: сигнализирует о начале и висит до отмены.
	paused := make(chan struct{}, 1)
	hang := func(time.Duration) <-chan time.Time {
		paused <- struct{}{}
		return make(chan time.Time)
	}
	done := make(chan struct{})
	go func() {
		sendLoop(ctx, s, box, budget, st, hang)
		close(done)
	}()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("first frame was not sent")
	}
	// Во время паузы приходит следующий кадр: после отмены он уйти не должен.
	box.put(gridFrame(t, 2, true, 300, 200, 2, gridAll...))
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendLoop did not exit when cancelled during the pause")
	}
	if n := len(fake.messages()); n != len(parts) {
		t.Fatalf("stream got %d messages, want only the first frame's %d parts", n, len(parts))
	}
	if st0, _, _ := s.snapshot(); st0 == StateEnded {
		t.Fatal("sendLoop must not end the session on cancel")
	}
}
