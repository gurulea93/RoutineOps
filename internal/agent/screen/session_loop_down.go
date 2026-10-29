//go:build screen

package screen

import (
	"context"
	"fmt"
	"log/slog"
)

// loopDown читает SessionDown с сервера и переводит команды захватчику (ключевой кадр,
// стоп). Область сеанса сервер может только СУЗИТЬ: проверяет агент сам, по приглашению,
// и попытка расширения игнорируется как событие безопасности, а не применяется.
func loopDown(ctx context.Context, s *session, st *sessionStream, ctl *ctlWriter, log *slog.Logger) {
	for {
		m, err := st.Recv()
		if err != nil {
			// Отменённый ctx рвёт и Recv: сеанс закрывает тот, кто отменил, а не мы.
			if ctx.Err() != nil {
				return
			}
			s.end(ReasonTransportLost, err.Error())
			return
		}
		if ctx.Err() != nil {
			return
		}
		c := m.GetControl()
		switch {
		case c.GetStop():
			// stop важнее request_keyframe в том же сообщении: кадр останавливаемому сеансу не нужен.
			s.end(stopReason(c.GetStopReason()))
			return
		case c.GetRequestKeyframe():
			if err := ctl.send(msgKeyframe, nil); err != nil {
				// Канал к захватчику умер — это заметит readPipe по EOF и закроет сеанс.
				log.Warn("screen: keyframe request not delivered to worker", "session_id", s.id, "err", err)
			}
		default:
			log.Debug("screen: server message ignored", "type", fmt.Sprintf("%T", m.GetMsg()), "session_id", s.id)
		}
	}
}

// maxStopDetail — потолок текста чужой причины в detail (в символах, не в байтах).
const maxStopDetail = 256

// stopReason — причина остановки от сервера. Текст пришёл по сети: из списка
// serverStopReasons берётся как есть, иначе OPERATOR_REVOKED, а сам текст — в detail,
// обрезанный по символам. Пустой — OPERATOR_REVOKED без detail.
func stopReason(raw string) (EndReason, string) {
	if raw == "" {
		return ReasonOperatorRevoked, ""
	}
	if r := EndReason(raw); serverStopReasons[r] {
		return r, ""
	}
	if rs := []rune(raw); len(rs) > maxStopDetail {
		raw = string(rs[:maxStopDetail])
	}
	return ReasonOperatorRevoked, raw
}
