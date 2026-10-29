//go:build !windows && !darwin

package tamper

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// На не-Windows пакет должен быть безопасным no-op: Arm не валит install,
// Disarm сообщает о неподдержке, Status/SafeMode дают нули.
func TestOtherNoop(t *testing.T) {
	if err := Arm(nil); err != nil {
		t.Fatalf("Arm на не-Windows должен быть no-op, получили: %v", err)
	}
	if err := Disarm(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Disarm должен вернуть ErrUnsupported, получили: %v", err)
	}
	if p, g, safe := Status(); p != 0 || g != 0 || safe {
		t.Fatalf("Status на не-Windows = (0,0,false), получили (%d,%d,%t)", p, g, safe)
	}
	if SafeMode() {
		t.Fatal("SafeMode на не-Windows должен быть false")
	}
	// Enforce должен немедленно вернуться (no-op), а не блокироваться.
	Enforce(context.Background(), nil)
	Cleanup()
}

// Unlock — примитив install-пути, а не пользовательская команда: на не-Windows он
// обязан быть ТИХИМ no-op. Верни он ErrUnsupported (как Disarm) — service.Install и
// relocateForService падали бы на каждой установке под Linux.
func TestOtherUnlockIsNoop(t *testing.T) {
	if err := Unlock(); err != nil {
		t.Fatalf("Unlock() = %v, хотим nil", err)
	}
	if err := Unlock("/usr/local/bin/RoutineOps-agent", "", "/nope"); err != nil {
		t.Fatalf("Unlock(paths) = %v, хотим nil", err)
	}
}

// ErrUnsupported больше не должен скатываться в «только Windows»: macOS-защита реальна.
func TestErrUnsupportedNamesSupportedPlatforms(t *testing.T) {
	msg := ErrUnsupported.Error()
	for _, want := range []string{"Windows", "macOS"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrUnsupported не упоминает %q: %q", want, msg)
		}
	}
}
