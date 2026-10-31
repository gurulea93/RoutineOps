//go:build screen

package screen

import (
	"context"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

// На стрим сеанса пишет только sendLoop: gRPC Send не допускает конкурентных вызовов.
// Остальные сообщения агента (SessionStats, SessionEnd) идут через тот же писатель или
// под общим с ним мьютексом, а не прямым stream.Send из другой горутины.
//
// Пока идёт пауза бюджета, новые кадры складываются в box (latestFrame), а не копятся
// очередью. after — источник таймера паузы (nil → time.After), для тестов.
func sendLoop(ctx context.Context, s *session, box *latestFrame, budget *bandwidthBudget, st *sessionStream,
	after func(time.Duration) <-chan time.Time) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-box.Ready():
		case <-ctx.Done():
			return
		}
		f := box.take()
		if f == nil {
			continue // лишний сигнал: кадр уже забран
		}
		n, err := sendParts(st.Send, *f)
		if err != nil {
			s.end(ReasonTransportLost, err.Error())
			return
		}
		_, stats, _ := s.snapshot()
		stats.Frames++
		stats.Bytes += uint64(n)
		stats.Stalls = budget.Stalls()
		stats.Dropped, _ = box.counters()
		s.setStats(stats)
		if p := s.nextProfile.Swap(nil); p != nil {
			prof, _ := ProfileByName(*p)
			budget.setProfile(prof)
		}
		pause, _ := budget.wait(n)
		if !sleepCtx(ctx, pause, after) {
			return
		}
	}
}

// sleepCtx ждёт d или отмены ctx: true — время вышло, false — отменили. d <= 0 — true
// сразу, без таймера и без взгляда на ctx. after — источник таймера (nil → time.After);
// тесты подставляют свой канал. Таймер time.After на пути отмены не держит ресурсов: с
// Go 1.23 неостановленный таймер без ссылок собирается сборщиком.
func sleepCtx(ctx context.Context, d time.Duration, after func(time.Duration) <-chan time.Time) bool {
	if d <= 0 {
		return true
	}
	if after == nil {
		after = time.After
	}
	select {
	case <-after(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// sendParts шлёт части кадра по порядку. Seq и Keyframe — из upFrame, LastPart — только
// на последней части, CapturedAtMs — момент захвата, который захватчик кладёт в каждую
// часть. Возвращает сумму payload отправленных частей; на первой ошибке send
// останавливается и возвращает её вместе с уже отправленными байтами.
func sendParts(send func(*pb.SessionUp) error, f upFrame) (bytes int, err error) {
	for i, p := range f.parts {
		err = send(&pb.SessionUp{Msg: &pb.SessionUp_Frame{Frame: &pb.SessionFrame{
			Seq:          f.seq,
			Keyframe:     f.key,
			LastPart:     i == len(f.parts)-1,
			Payload:      p.Payload,
			CapturedAtMs: p.CapturedMs,
		}}})
		if err != nil {
			return bytes, err
		}
		bytes += len(p.Payload)
	}
	return bytes, nil
}
