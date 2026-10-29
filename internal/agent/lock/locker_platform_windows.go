//go:build windows

package lock

import "log/slog"

// NewPlatformLocker — Locker для службы под текущей ОС. На Windows это
// SessionLocker: служба сама поднимает полноэкранный оверлей в активной сессии
// (не зависит от трея). exe — путь к бинарю агента (os.Executable).
func NewPlatformLocker(exe string, log *slog.Logger) Locker {
	return NewSessionLocker(exe, log)
}
