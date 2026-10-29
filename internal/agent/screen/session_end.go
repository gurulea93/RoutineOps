//go:build screen

package screen

import (
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Floodww/RoutineOps/internal/agent/outbox"
	pb "github.com/Floodww/RoutineOps/proto"
)

// eventSink — durable-очередь агента (*outbox.Queue). Узкий интерфейс — ради тестов.
type eventSink interface {
	Enqueue(kind string, data []byte) error
}

// endSendTimeout — сколько ждём SessionEnd на стриме. Это лишь вежливое уведомление
// сервера: истина о конце сеанса уже лежит в очереди, поэтому зависший стрим не должен
// держать закрытие захватчика и плашки.
const endSendTimeout = 2 * time.Second

// finishSession — единственная точка закрытия сеанса на стороне агента. Зовётся ОДИН раз,
// после выхода ПИШУЩИХ циклов (sendLoop): стрим допускает только одного писателя, и
// SessionEnd пишется здесь уже без гонки с кадрами. Читающие циклы (loopDown, readPipe)
// ждать не нужно: их освободит st.close() ниже. Стрим живёт на собственном контексте
// (session_dial.go), поэтому s.end, отменивший s.ctx, SessionEnd не ломает.
//
// Порядок важен:
//  1. Событие «end» уходит в durable-очередь ПЕРВЫМ (protected, §9.20): аудит терминального
//     исхода не зависит ни от сети, ни от того, умер ли захватчик чисто. Не удалось
//     записать — это громкая ошибка в лог, но закрытие всё равно идёт дальше: оставлять
//     плашку и захват включёнными из-за сбоя диска нельзя.
//  2. Захватчик закрывается (stdin → ожидание → kill): вместе с ним гаснет плашка.
//  3. SessionEnd на стрим — с потолком времени и без повторов.
//  4. Закрытие стрима и соединения.
//
// Если сеанс ещё не закрыт (циклы вышли по отмене извне), причина — TRANSPORT_LOST: без
// неё в аудите остался бы сеанс без исхода.
func finishSession(s *session, st *sessionStream, w *workerProc, q eventSink, log *slog.Logger, now func() time.Time) {
	s.end(ReasonTransportLost, "session loops exited without a reason")
	_, stats, detail := s.snapshot()
	s.mu.Lock()
	reason := s.endReason
	s.mu.Unlock()
	detail = truncRunes(detail, maxStopDetail)

	if now == nil {
		now = time.Now
	}
	ev := &pb.ScreenEvent{
		SessionId: s.id, Kind: "end", Reason: string(reason), Detail: detail, AtMs: now().UnixMilli(),
	}
	if q == nil {
		log.Error("screen: no event queue, terminal event lost", "session_id", s.id, "reason", string(reason))
	} else if data, err := proto.Marshal(ev); err != nil {
		log.Error("screen: terminal event not encoded", "session_id", s.id, "err", err)
	} else if err := q.Enqueue(outbox.KindScreenSession, data); err != nil {
		log.Error("screen: terminal event not queued", "session_id", s.id, "reason", string(reason), "err", err)
	}

	if w != nil {
		if err := w.close(); err != nil {
			log.Debug("screen: worker exit", "session_id", s.id, "err", err)
		}
	}

	if st != nil {
		sent := make(chan error, 2) // горутина пишет два значения и не должна зависнуть после таймаута
		go func() {
			sent <- st.Send(&pb.SessionUp{Msg: &pb.SessionUp_Stats{Stats: &pb.SessionStats{
				Frames: stats.Frames, Bytes: stats.Bytes, Stalls: stats.Stalls, Dropped: stats.Dropped,
			}}})
			sent <- st.Send(&pb.SessionUp{Msg: &pb.SessionUp_End{End: &pb.SessionEnd{Reason: string(reason), Detail: detail}}})
		}()
		timer := time.NewTimer(endSendTimeout)
		defer timer.Stop()
		for i := 0; i < 2; i++ {
			select {
			case err := <-sent:
				if err != nil {
					log.Debug("screen: end message not delivered", "session_id", s.id, "err", err)
					i = 2
				}
			case <-timer.C:
				log.Debug("screen: end message timed out", "session_id", s.id)
				i = 2
			}
		}
		_ = st.CloseSend()
		st.close() // отмена стрима рвёт и зависший Send из горутины выше
	}
}

// truncRunes обрезает по символам, а не байтам, чтобы не порвать UTF-8.
func truncRunes(raw string, max int) string {
	if rs := []rune(raw); len(rs) > max {
		return string(rs[:max])
	}
	return raw
}
