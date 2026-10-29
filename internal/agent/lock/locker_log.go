package lock

import "log/slog"

// LogLocker — заглушка Locker: только логирует подъём/снятие замка, экран НЕ
// блокирует. Безопасный плейсхолдер для проводки и неинтерактивных сред (сервер,
// CI) до появления настоящего полноэкранного оверлея с полем пароля
// (platform-специфичный GUI — следующий шаг). Хранит verify-колбэк, чтобы внешний
// триггер (напр. CLI-команда `agent unlock`) мог проверить пароль.
type LogLocker struct {
	log    *slog.Logger
	verify func(string) bool
}

// NewLogLocker создаёт лог-заглушку.
func NewLogLocker(log *slog.Logger) *LogLocker { return &LogLocker{log: log} }

func (l *LogLocker) Show(reason string, verify func(string) bool) {
	l.verify = verify
	l.log.Warn("lock: замок поднят (заглушка — экран не блокируется)", slog.String("reason", reason))
}

func (l *LogLocker) Hide() {
	l.verify = nil
	l.log.Warn("lock: замок снят (заглушка)")
}

// TryUnlock проверяет пароль через сохранённый verify-колбэк (для внешнего
// триггера разблокировки, пока нет интерактивного оверлея). false, если замок не
// поднят.
func (l *LogLocker) TryUnlock(password string) bool {
	if l.verify == nil {
		return false
	}
	return l.verify(password)
}
