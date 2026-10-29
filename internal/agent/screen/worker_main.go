//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// RunWorkerProcess — тело подкоманды `agent screen-worker`. Первым сообщением служба шлёт
// msgConfig (параметры не едут аргументами командной строки: её видит любой локальный
// пользователь), дальше идёт RunWorker. Возвращает код выхода процесса: 0 — сеанс
// отработал и сообщил причину по каналу, 1 — службе нечего было сообщить (канал не наш).
func RunWorkerProcess(ctx context.Context, in io.Reader, out io.Writer) int {
	return runWorkerProcess(ctx, in, out, NewScreenCapturer, ShowNotice)
}

func runWorkerProcess(ctx context.Context, in io.Reader, out io.Writer,
	newCapturer func() (Capturer, error), showNotice func(operator, reason string) (Notice, error)) int {
	typ, body, err := readMsg(in)
	var cfg wireConfig
	if err != nil || typ != msgConfig || json.Unmarshal(body, &cfg) != nil {
		_ = writeJSONMsg(out, msgEnd, wireEnd{Reason: string(ReasonProtocolError), Detail: "first message is not a valid config"})
		return 1
	}
	RunWorker(ctx, WorkerConfig{
		Profile:     cfg.Profile,
		Operator:    cfg.Operator,
		Reason:      cfg.Reason,
		MaxDuration: time.Duration(cfg.MaxDurationSec) * time.Second,
		NewCapturer: newCapturer,
		ShowNotice:  showNotice,
	}, in, out)
	return 0
}
