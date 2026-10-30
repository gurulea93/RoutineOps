//go:build screen

package screen

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
)

const maxFrameParts = 64 // потолок частей кадра (64 × 256 KiB): кадр без last_part не копит память

type pipeMsg struct {
	typ  byte
	body []byte
	err  error
}

// readPipe кладёт целые проверенные кадры захватчика в box. Конец: msgEnd, EOF (WORKER_LOST) или отмена
// ctx (закрывает отменивший). Read отменой не прерывается: горутина выйдет, когда закроют процесс.
func readPipe(ctx context.Context, s *session, src io.Reader, box *latestFrame, log *slog.Logger) EndReason {
	msgs := make(chan pipeMsg)
	go func() {
		for {
			typ, body, err := readMsg(src)
			select {
			case msgs <- pipeMsg{typ, body, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	cur, last := []wireFrame(nil), uint32(0)
	for {
		var m pipeMsg
		select {
		case <-ctx.Done():
			return ""
		case m = <-msgs:
		}
		if m.err != nil {
			r, _ := s.end(ReasonWorkerLost, m.err.Error())
			return r
		}
		switch m.typ {
		case msgFrame:
			f, err := parseFrameMsg(m.body)
			if err != nil || (len(cur) > 0 && f.Seq != cur[0].Seq) || len(cur) >= maxFrameParts {
				box.reject()
				cur = nil
				continue
			}
			if cur = append(cur, f); f.Last {
				if checkParts(cur) != nil || f.Seq <= last {
					box.reject()
				} else {
					last = f.Seq
					box.put(&upFrame{seq: f.Seq, key: f.Keyframe, parts: cur})
				}
				cur = nil
			}
		case msgEnd:
			var e wireEnd
			_ = json.Unmarshal(m.body, &e)
			r, _ := s.end(EndReason(cmp.Or(e.Reason, string(ReasonWorkerLost))), e.Detail)
			return r
		case msgSignal:
			if s.onSignal != nil {
				s.onSignal(m.body)
			}
		case msgHello, msgStats: // hello — старт сеанса; счётчики служба ведёт сама
		default:
			log.Warn("screen: unknown worker message ignored", "type", m.typ, "session_id", s.id)
		}
	}
}

// reject — кадр захватчика не прошёл проверку: не уходит, считается ошибкой.
func (l *latestFrame) reject() { l.mu.Lock(); l.errors++; l.mu.Unlock() }
