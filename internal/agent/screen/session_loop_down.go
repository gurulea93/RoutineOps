//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

// primaryDisplay — идентификатор основного экрана. Пустой список displays в приглашении
// означает «только основной», а не «все» (см. ScreenSessionCommand.displays в proto).
// В M1 экран один, поэтому основной — 0.
const primaryDisplay int32 = 0

// loopDown читает SessionDown с сервера и переводит команды захватчику (ключевой кадр,
// стоп). Область сеанса сервер может только СУЗИТЬ: проверяет агент сам, по приглашению,
// и попытка расширения игнорируется как событие безопасности, а не применяется.
func loopDown(ctx context.Context, s *session, st *sessionStream, ctl *ctlWriter, log *slog.Logger) {
	// SessionInput в M1 не бывает вовсе (сеанс только на просмотр). Ввод идёт десятками
	// событий в секунду, поэтому предупреждение — один раз на сеанс, а не на событие.
	inputWarned := false
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
		if in := m.GetInput(); in != nil {
			if !s.inputOn.Load() {
				if !inputWarned {
					inputWarned = true
					log.Warn("screen: SessionInput ignored, view-only session", "session_id", s.id)
				}
				continue
			}
			forwardInput(s, ctl, in, log)
			continue
		}
		c := m.GetControl()
		handled := false
		if c.GetStop() {
			// stop важнее narrow и request_keyframe в том же сообщении: останавливаемому
			// сеансу кадр и сужение области не нужны.
			s.end(stopReason(c.GetStopReason()))
			return
		}
		if n := c.GetNarrow(); n != nil {
			handled = true
			if !handleNarrow(s, n, ctl, log) {
				return
			}
		}
		if c.GetRequestKeyframe() {
			handled = true
			if err := ctl.send(msgKeyframe, nil); err != nil {
				// Канал к захватчику умер — это заметит readPipe по EOF и закроет сеанс.
				log.Warn("screen: keyframe request not delivered to worker", "session_id", s.id, "err", err)
			}
		}
		if !handled {
			log.Debug("screen: server message ignored", "type", fmt.Sprintf("%T", m.GetMsg()), "session_id", s.id)
		}
	}
}

// narrowVerdict — вердикт по запросу SessionControl.narrow.
type narrowVerdict int

const (
	narrowOK     narrowVerdict = iota // подмножество приглашения, остаётся хотя бы один экран
	narrowEmpty                       // не оставляет ни одного экрана
	narrowExpand                      // просит больше, чем дало приглашение
)

// invitedDisplays — экраны, разрешённые приглашением. Пустой список — только основной.
func invitedDisplays(cmd *pb.ScreenSessionCommand) map[int32]bool {
	set := make(map[int32]bool)
	for _, d := range cmd.GetDisplays() {
		set[d] = true
	}
	if len(set) == 0 {
		set[primaryDisplay] = true
	}
	return set
}

// checkNarrow сверяет запрос сервера с приглашением. Чистая функция: решает только, что это
// за запрос, и ничего не применяет.
//
// Расширение проверяется РАНЬШЕ пустоты: запрос, который и просит лишнего, и ничего не
// оставляет, — попытка расширения, а не штатное закрытие сеанса.
//
// Пустой список displays в narrow читается как «не остаётся ни одного экрана» (а не как
// «основной», как в приглашении): сужение до основного выражается явным списком [0].
// Сервер сегодня narrow не шлёт; когда начнёт (возврат управления в Ф3), этот смысл надо
// сверить с тем, что он кладёт в displays.
func checkNarrow(cmd *pb.ScreenSessionCommand, req *pb.ScreenScope) narrowVerdict {
	if req.GetAllowInput() && !cmd.GetAllowInput() {
		return narrowExpand
	}
	invited := invitedDisplays(cmd)
	for _, d := range req.GetDisplays() {
		if !invited[d] {
			return narrowExpand
		}
	}
	if len(req.GetDisplays()) == 0 {
		return narrowEmpty
	}
	return narrowOK
}

// handleNarrow применяет вердикт. Ложь — сеанс закрыт, цикл должен выйти.
//
// TODO: попытку расширения стоит отправлять событием безопасности (outbox.KindSecurity,
// pb.SecurityEvent). Сюда оно пока не проведено: у loopDown нет доступа к очереди, а
// пробрасывать её надо вместе с session_end.go, одним решением. До тех пор — только лог.
func handleNarrow(s *session, req *pb.ScreenScope, ctl *ctlWriter, log *slog.Logger) bool {
	// Смена качества едет в том же сообщении (proto не менялся): коды ≥ profileCodeBase в
	// displays — это профиль, а не экран. Снимаем их до проверки приглашения.
	req, profile := splitProfileCode(req)
	if profile != "" && ctl != nil {
		b, _ := json.Marshal(wireProfile{Name: profile})
		if err := ctl.send(msgProfile, b); err != nil {
			log.Warn("screen: profile change not delivered", "session_id", s.id, "err", err)
		}
	}
	if len(req.GetDisplays()) == 0 && profile != "" {
		return true
	}
	switch checkNarrow(s.cmd, req) {
	case narrowExpand:
		log.Warn("screen: expansion attempt rejected", "session_id", s.id,
			"displays", req.GetDisplays(), "allow_input", req.GetAllowInput())
		return true
	case narrowEmpty:
		s.end(ReasonOperatorRevoked, "narrow left no displays")
		return false
	}
	// Возврат управления — сужение: ввод перестаёт исполняться, зажатое отпускается. Назад
	// не включается: взять управление снова — новый сеанс с новой строкой аудита.
	if s.inputOn.Load() && !req.GetAllowInput() {
		s.inputOn.Store(false)
		if ctl != nil {
			forwardInput(s, ctl, &pb.SessionInput{ReleaseAll: true}, log)
		}
		log.Info("screen: control returned to the employee", "session_id", s.id)
	}
	// Один выбранный экран из приглашённых — это переключение монитора: захватчик
	// пересоздаётся. Список из нескольких экранов оставляет текущий как есть.
	if ds := req.GetDisplays(); len(ds) == 1 && ctl != nil {
		b, _ := json.Marshal(wireDisplay{Index: int(ds[0])})
		if err := ctl.send(msgDisplay, b); err != nil {
			log.Warn("screen: display switch not delivered", "session_id", s.id, "err", err)
		}
	}
	log.Debug("screen: narrow accepted", "session_id", s.id,
		"displays", req.GetDisplays(), "allow_input", req.GetAllowInput())
	return true
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

// forwardInput передаёт пачку ввода захватчику. Ошибка канала не поднимается: умерший
// захватчик заметит readPipe по EOF и закроет сеанс.
func forwardInput(s *session, ctl *ctlWriter, in *pb.SessionInput, log *slog.Logger) {
	b, err := proto.Marshal(in)
	if err != nil || len(b) > maxWireMsg {
		log.Warn("screen: SessionInput dropped", "session_id", s.id, "err", err, "bytes", len(b))
		return
	}
	if err := ctl.send(msgInput, b); err != nil {
		log.Warn("screen: input not delivered to worker", "session_id", s.id, "err", err)
	}
}
