//go:build screen

package main

import (
	"log/slog"
	"os"
	"strings"

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
	var ips []string
	for _, ip := range strings.Split(os.Getenv("SCREEN_RTC_PUBLIC_IPS"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			ips = append(ips, ip)
		}
	}
	svc, err := screen.New(screen.Config{
		DB: db, Registry: reg, RecordingDir: dir, Logger: logger,
		// WebRTC (VP8) включается только когда задан порт; агенты без libvpx его всё равно не
		// объявляют, и такие сеансы идут по RSF1.
		RTCUDPAddr: os.Getenv("SCREEN_RTC_UDP"), RTCPublicIPs: ips,
		BuildAdminEmail: os.Getenv("SEED_ADMIN_EMAIL"), BuildsDir: os.Getenv("BUILDS_DIR"),
	})
	if err != nil {
		logger.Error("remote desktop: service not started", "err", err)
		os.Exit(1)
	}
	g.RegisterScreenService(svc)
	logger.Info("remote desktop enabled (RSF1, view-only)", "recording_dir", dir)
	return svc.Routes()
}
