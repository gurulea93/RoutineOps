//go:build screen

package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Floodww/RoutineOps/internal/agent/command"
	"github.com/Floodww/RoutineOps/internal/agent/screen"
	"github.com/Floodww/RoutineOps/internal/agent/selfupdate"
	"github.com/Floodww/RoutineOps/internal/agent/transport"
)

// Проводка удалённого стола (тег screen). Способность screen_session объявляется только
// здесь: free-сборка не содержит ни захватчика, ни менеджера сеанса, и сервер, увидев
// отсутствие способности, сам отвечает оператору 409 agent_unsupported до создания сеанса.
type screenRuntime struct {
	svc *screen.Service
	log *slog.Logger
}

// wireScreen: первый параметр (машинный каталог состояния) не нужен — канал со
// захватчиком идёт пайпами процесса, а не сокетом на диске. exe — бинарь агента, им же
// запускается screen-worker.
func wireScreen(_, exe string, dialer *transport.Dialer, enqueue func(string, []byte) error, log *slog.Logger) *screenRuntime {
	svc := screen.NewAgentService(context.Background(), screen.ServiceConfig{
		Exe: exe, Version: version, Dialer: dialer, Enqueue: enqueue, WorkerLog: workerLog{log},
	}, log)
	return &screenRuntime{svc: svc, log: log}
}

func (r *screenRuntime) capabilities() []string { return []string{"screen_session"} }

func (r *screenRuntime) sessioner() command.ScreenSessioner { return r.svc }

// holdUpdates не даёт самообновлению заменить бинарь посреди сеанса (контракт §9.1). По
// исчерпании отсрочки сеанс закрывается штатно с причиной AGENT_UPDATE.
func (r *screenRuntime) holdUpdates(u *selfupdate.Updater) {
	u.SetHold(selfupdate.Hold{
		Active: func() (bool, string) {
			if id := r.svc.ActiveID(); id != "" {
				return true, "screen session " + id
			}
			return false, ""
		},
		Release: func() { r.svc.Release(screen.ReasonAgentUpdate) },
	})
}

// workerLog пишет stderr захватчика в лог службы построчно.
type workerLog struct{ log *slog.Logger }

func (w workerLog) Write(p []byte) (int, error) {
	if s := strings.TrimSpace(string(p)); s != "" {
		w.log.Info("screen-worker", "stderr", s)
	}
	return len(p), nil
}
