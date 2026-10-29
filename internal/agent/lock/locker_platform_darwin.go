//go:build darwin

package lock

import "log/slog"

// NewPlatformLocker — Locker для службы под текущей ОС. На macOS
// поднимаем полноэкранный оверлей в активной сессии. exe — путь к бинарю
// агента (os.Executable).
func NewPlatformLocker(exe string, log *slog.Logger) Locker {
	return NewSessionLocker(exe, log)
}
