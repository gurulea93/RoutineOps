//go:build !windows && !darwin

package tamper

import (
	"context"
	"errors"
	"log/slog"
)

// ErrUnsupported — tamper-protection реализована на Windows (SafeBoot, SCM, реестр)
// и на macOS (immutable-флаг schg, см. tamper_darwin.go). Эта сборка — всё остальное
// (Linux): защиты нет вовсе, и разоружение честно говорит об этом, а не притворяется
// успешным — иначе оператор поверит, что что-то снял.
var ErrUnsupported = errors.New("tamper-protection на этой ОС не реализована (поддерживаются Windows и macOS)")

// Arm — no-op на Linux: install не должен падать там, где защиты нет.
func Arm(*slog.Logger) error { return nil }

// Disarm возвращает ErrUnsupported, чтобы CLI напечатал понятную причину.
func Disarm() error { return ErrUnsupported }

// Unlock — no-op: на Linux/BSD tamper-protection не реализована (Arm тоже no-op),
// immutable-флагов агент не ставит — снимать нечего. Возвращать ErrUnsupported, как
// делает Disarm, здесь НЕЛЬЗЯ: Unlock зовут install-пути (service.Install,
// relocateForService), и ошибка валила бы установку на Linux на ровном месте.
// Если защиту здесь однажды сделают (chattr +i), снятие приедет сюда — вызовы уже
// расставлены по всем точкам записи.
func Unlock(...string) error { return nil }

// Cleanup — no-op.
func Cleanup() {}

// Enforce — no-op: на не-Windows защиты нет, сторож не нужен.
func Enforce(context.Context, *slog.Logger) {}

// Status возвращает нули и safeMode=false.
func Status() (protection, safeBoot uint32, safeMode bool) { return 0, 0, false }

// SafeMode на не-Windows всегда false.
func SafeMode() bool { return false }
