package transport

import (
	"testing"
	"time"
)

func TestBackoffWithinBounds(t *testing.T) {
	const max = 60 * time.Second
	b := newBackoff(1*time.Second, max)
	for i := 0; i < 50; i++ {
		d := b.next()
		if d < 0 || d > max {
			t.Fatalf("delay %v вне диапазона [0, %v]", d, max)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	base := 1 * time.Second
	b := newBackoff(base, 60*time.Second)
	for i := 0; i < 10; i++ {
		b.next() // разгоняем cur до потолка
	}
	b.reset()
	// После reset cur == base, поэтому первый jitter не превышает base.
	if d := b.next(); d > base {
		t.Fatalf("после reset первая задержка %v > base %v", d, base)
	}
}
