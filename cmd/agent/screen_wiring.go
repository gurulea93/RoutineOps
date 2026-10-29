//go:build screen

package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Floodww/RoutineOps/internal/agent/command"
	"github.com/Floodww/RoutineOps/internal/agent/screen"
	"github.com/Floodww/RoutineOps/internal/agent/selfupdate"
	"github.com/Floodww/RoutineOps/internal/agent/transport"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Проводка удалённого стола (тег screen). Пока менеджер сеанса не написан, агент
// отказывает в любом приглашении кодом, а не молчит, и НЕ объявляет способность
// screen_session: сервер тогда сам отвечает оператору 409 agent_unsupported ещё до
// создания сеанса.
type screenRuntime struct {
	mgr command.ScreenSessioner
	log *slog.Logger
}

func wireScreen(_, _ string, _ *transport.Dialer, _ func(string, []byte) error, log *slog.Logger) *screenRuntime {
	return &screenRuntime{mgr: refusingSessioner{}, log: log}
}

func (r *screenRuntime) capabilities() []string { return nil }

func (r *screenRuntime) sessioner() command.ScreenSessioner { return r.mgr }

func (r *screenRuntime) holdUpdates(_ *selfupdate.Updater) {}

// refusingSessioner — временная заглушка до менеджера сеанса.
type refusingSessioner struct{}

func (refusingSessioner) StartSession(_ context.Context, _ string, _ *pb.ScreenSessionCommand) (string, error) {
	return string(screen.ReasonCaptureUnsupported), errors.New("менеджер сеанса экрана ещё не реализован в этой сборке")
}
