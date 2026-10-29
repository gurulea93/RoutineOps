package api

import (
	"sync"
	"time"
)

// loginLimiter — per-account backoff против распределённого брутфорса логина (D).
// Дополняет per-IP httprate (handler.go): считает НЕудачные попытки по ключу
// (email в нижнем регистре), и после max подряд блокирует аккаунт на lockFor,
// независимо от IP-источника. Окно window сбрасывает счётчик, если атак нет.
// In-memory: прод single-instance; при рестарте счётчики обнуляются (не критично —
// bcrypt-cost-12 всё равно держит скорость перебора низкой).
// Границы против attacker-keyed роста мапы attempts (pre-auth OOM): ключ — email из
// тела запроса, поэтому усекаем его длину (иначе ~1MB email = ~1MB ключ навсегда) и
// держим верхний потолок числа ключей (свип истёкших + отказ трекать новый при полной
// мапе; per-IP httprate на /login остаётся).
const (
	maxLockKeyLen = 254  // RFC 5321 email cap
	maxLockKeys   = 4096 // потолок: 4096 × 254B ≈ 1MB
)

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
	max      int
	window   time.Duration
	lockFor  time.Duration
}

// normLockKey усекает ключ до RFC-максимума email, чтобы гигантское тело не осело
// ключом мапы.
func normLockKey(key string) string {
	if len(key) > maxLockKeyLen {
		return key[:maxLockKeyLen]
	}
	return key
}

// evictExpired удаляет ключи с истёкшим окном и снятым блоком. Вызывать под l.mu.
func (l *loginLimiter) evictExpired(now time.Time) {
	for k, a := range l.attempts {
		if now.After(a.lockedUntil) && now.Sub(a.first) > l.window {
			delete(l.attempts, k)
		}
	}
}

type loginAttempt struct {
	count       int
	first       time.Time
	lockedUntil time.Time
}

func newLoginLimiter(max int, window, lockFor time.Duration) *loginLimiter {
	return &loginLimiter{
		attempts: make(map[string]*loginAttempt),
		max:      max,
		window:   window,
		lockFor:  lockFor,
	}
}

// locked сообщает, заблокирован ли ключ сейчас, и до какого момента.
func (l *loginLimiter) locked(key string, now time.Time) (bool, time.Time) {
	key = normLockKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[key]
	if a != nil && now.Before(a.lockedUntil) {
		return true, a.lockedUntil
	}
	return false, time.Time{}
}

// fail регистрирует неудачную попытку; при достижении max в пределах window
// ставит блок на lockFor и сбрасывает счётчик.
func (l *loginLimiter) fail(key string, now time.Time) {
	key = normLockKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[key]
	if a == nil || now.Sub(a.first) > l.window {
		if a == nil {
			if len(l.attempts) >= maxLockKeys {
				l.evictExpired(now)
			}
			if len(l.attempts) >= maxLockKeys {
				return // мапа полна даже после свипа: новый ключ не трекаем (per-IP лимит остаётся)
			}
		}
		a = &loginAttempt{first: now}
		l.attempts[key] = a
	}
	a.count++
	if a.count >= l.max {
		a.lockedUntil = now.Add(l.lockFor)
		a.count = 0
		a.first = now
	}
}

// success очищает счётчик при удачном входе.
func (l *loginLimiter) success(key string) {
	key = normLockKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
