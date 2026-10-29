//go:build screen

package main

import (
	"log/slog"
	"os"

	"github.com/Floodww/RoutineOps/internal/server/api"
	"github.com/Floodww/RoutineOps/internal/server/gateway"
	"github.com/Floodww/RoutineOps/internal/server/registry"
	"github.com/Floodww/RoutineOps/internal/server/screen"
	"github.com/Floodww/RoutineOps/internal/server/storage"
)

// screenSetup — удалённый рабочий стол (Этап A, тег screen). Сервис регистрируется на
// gateway (стрим сеанса и события агента) и отдаёт ручки панели вместе с purger'ом.
//
// Не стартовать сервис при ошибке — правильно: без каталога записей сеанс не может
// начаться вовсе (запись обязательна), и молча собранный «стол без записи» хуже, чем
// громкий отказ при старте.
func screenSetup(g *gateway.Gateway, db *storage.DB, reg *registry.Registry, dir string, logger *slog.Logger) []api.RouterOption {
	svc, err := screen.New(screen.Config{DB: db, Registry: reg, RecordingDir: dir, Logger: logger})
	if err != nil {
		logger.Error("remote desktop: service not started", "err", err)
		os.Exit(1)
	}
	g.RegisterScreenService(svc)
	logger.Info("remote desktop enabled (RSF1, view-only)", "recording_dir", dir)
	return svc.Routes()
}
