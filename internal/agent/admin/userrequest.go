package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UserRequest — заявка сотрудника на временные админ-права. Трей (юзер-сессия)
// кладёт её файлом, служба подхватывает и шлёт RequestAdminAccess: у трея нет
// машинной mTLS-идентичности (session-0 isolation), поэтому RPC делает служба.
type UserRequest struct {
	Reason      string `json:"reason"`
	RequestedAt int64  `json:"requested_at"`
}

// WriteUserRequest кладёт заявку файлом (вызывает трей по нажатию кнопки).
func WriteUserRequest(path, reason string) error {
	data, err := json.Marshal(UserRequest{Reason: reason, RequestedAt: time.Now().Unix()})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadUserRequest читает заявку (вызывает служба). os.ErrNotExist = заявки нет.
func ReadUserRequest(path string) (UserRequest, error) {
	var r UserRequest
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}

// WatchUserRequests опрашивает файл-заявку: при появлении шлёт RequestAdminAccess.
// Запускать в службе (у неё есть mTLS-идентичность).
func WatchUserRequests(ctx context.Context, dialer *transport.Dialer, path string, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	send := func(reason string) error { return RequestAdmin(ctx, dialer, reason, log) }
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			processRequest(path, send, log)
		}
	}
}

// processRequest обрабатывает один тик: читает заявку-файл и пытается отправить.
// send — отправитель (в проде RequestAdmin), выделен сеймом для тестов.
//
// Судьба файла-заявки:
//   - успех            → удалить (доставлено);
//   - терминальный код → удалить (сервер отказал окончательно, повтор бессмыслен —
//     иначе заявка спамит RequestAdminAccess каждый тик, см. isTerminalRequestErr);
//   - транзиент        → оставить (до-слать на следующем тике).
func processRequest(path string, send func(reason string) error, log *slog.Logger) {
	req, err := ReadUserRequest(path)
	if err != nil {
		return // заявки нет (или файл недоступен)
	}
	switch sendErr := send(req.Reason); {
	case sendErr == nil:
		log.Info("admin: заявка на права из трея отправлена", slog.String("reason", req.Reason))
	case isTerminalRequestErr(sendErr):
		log.Error("admin: заявка отклонена сервером окончательно — снимаю (повтор не поможет)",
			slog.String("code", status.Code(sendErr).String()),
			slog.Any("error", sendErr))
	default:
		log.Error("admin: заявка из трея не отправлена — повторю", slog.Any("error", sendErr))
		return // транзиент: оставляем файл
	}
	if err := os.Remove(path); err != nil {
		log.Warn("admin: заявка-файл не удалён", slog.Any("error", err))
	}
}

// isTerminalRequestErr сообщает, что сервер вынес окончательный вердикт по заявке
// и повтор бессмыслен (poison pill, ср. reportErr в cmd/agent):
//
//   - NotFound          — устройство/заявка уже не существует;
//   - InvalidArgument   — заявка не пройдёт валидацию ни при каком повторе;
//   - FailedPrecondition— неустранимое предусловие (напр. устройству не назначен
//     владелец — admin-заявку некому маршрутизировать, пока IT не назначит owner).
//
// Всё остальное (Unavailable, обрыв связи, Unknown от dial-фейла) — транзиент.
func isTerminalRequestErr(err error) bool {
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}
