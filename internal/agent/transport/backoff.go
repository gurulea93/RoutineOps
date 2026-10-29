package transport

import (
	"math/rand"
	"time"
)

// backoff — экспоненциальная задержка переподключения с jitter и потолком.
// Полный jitter: sleep ∈ [0, min(cap, base*2^attempt)].
type backoff struct {
	base   time.Duration
	max    time.Duration
	factor float64
	cur    time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	return &backoff{base: base, max: max, factor: 2, cur: base}
}

// next возвращает следующую задержку и продвигает счётчик.
func (b *backoff) next() time.Duration {
	d := b.cur
	if d > b.max {
		d = b.max
	}
	// Полный jitter — размазываем переподключения, чтобы парк агентов
	// не долбил сервер синхронно после общего обрыва.
	jittered := time.Duration(rand.Int63n(int64(d) + 1))

	b.cur = time.Duration(float64(b.cur) * b.factor)
	if b.cur > b.max {
		b.cur = b.max
	}
	return jittered
}

// reset возвращает задержку к базовой (после успешного соединения).
func (b *backoff) reset() {
	b.cur = b.base
}
