//go:build !windows && (!darwin || !cgo)

package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Floodww/RoutineOps/internal/agent/config"
)

// runTray — заглушка на Linux и на CGO=0-сборках macOS (кросс-компиляция без
// Cocoa, см. build-mac в Makefile): fyne.io/systray на Darwin требует cgo, без
// него трей собрать нельзя — как и lockui (см. lockui_other.go).
func runTray(_ *config.Config) {
	fmt.Fprintln(os.Stderr, "подкоманда tray поддерживается только на Windows и macOS (сборка с CGO)")
	os.Exit(2)
}

// launchTrayInActiveSession — no-op вне Windows/macOS(cgo).
func launchTrayInActiveSession(_ *slog.Logger) {}

// relaunchTrayAtServiceStart — no-op вне Windows/macOS(cgo): трея нет.
func relaunchTrayAtServiceStart(_ *slog.Logger) {}
