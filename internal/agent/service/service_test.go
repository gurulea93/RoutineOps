package service

import (
	"errors"
	"fmt"
	"testing"
)

// ErrTrayInstallFailed и ErrServiceStartFailed — разные сентинелы: runEnroll логирует
// их разными Warn-ветками. Тест страхует от «упрощения» вида
// ErrTrayInstallFailed = ErrServiceStartFailed, которое перепутало бы ветки.
func TestErrTrayInstallFailed_IsDistinctSentinel(t *testing.T) {
	if errors.Is(ErrTrayInstallFailed, ErrServiceStartFailed) ||
		errors.Is(ErrServiceStartFailed, ErrTrayInstallFailed) {
		t.Fatal("сентинелы tray/start не должны путаться через errors.Is")
	}
	wrapped := fmt.Errorf("обёртка: %w", ErrTrayInstallFailed)
	if !errors.Is(wrapped, ErrTrayInstallFailed) {
		t.Fatal("errors.Is должен находить ErrTrayInstallFailed сквозь обёртку")
	}
	if errors.Is(wrapped, ErrServiceStartFailed) {
		t.Fatal("обёрнутый tray-сентинел не должен матчиться на start-сентинел")
	}
}
