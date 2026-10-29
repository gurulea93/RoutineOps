//go:build !screen

package main

import (
	"log/slog"

	"github.com/Floodww/RoutineOps/internal/server/api"
	"github.com/Floodww/RoutineOps/internal/server/gateway"
	"github.com/Floodww/RoutineOps/internal/server/registry"
	"github.com/Floodww/RoutineOps/internal/server/storage"
)

// Без тега screen удалённого стола нет: стрим сеанса отвечает Unimplemented, ручек
// /screen-* не существует.
func screenSetup(_ *gateway.Gateway, _ *storage.DB, _ *registry.Registry, _ string, _ *slog.Logger) []api.RouterOption {
	return nil
}
