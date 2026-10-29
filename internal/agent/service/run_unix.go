//go:build !windows

package service

import (
	"context"
	"os/signal"
	"syscall"
)

// Run выполняет work до SIGINT/SIGTERM. launchd при остановке службы шлёт SIGTERM,
// поэтому отдельной интеграции с менеджером служб на Unix не требуется.
func Run(work func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return work(ctx)
}
