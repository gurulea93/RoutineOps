//go:build screen

package screen

import (
	"testing"
	"time"
)

// fakeClock — ручное время: тесты бюджета не спят.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

const lowRate = 187_500 // 1.5 Мбит/с в байтах

func newTestBudget() (*bandwidthBudget, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_000_000, 0)}
	return newBandwidthBudget(ProfileLow, c.now), c
}

func TestBudgetLowProfileValues(t *testing.T) {
	if ProfileLow.MaxBitsPerSec != 1_500_000 || ProfileLow.MaxPause != time.Second {
		t.Fatalf("low budget %+v", ProfileLow)
	}
	b, _ := newTestBudget()
	if b.rate != lowRate || b.maxPause != time.Second {
		t.Fatalf("rate %v, max pause %v", b.rate, b.maxPause)
	}
}

func TestBudgetUnderCapNoPause(t *testing.T) {
	b, _ := newTestBudget()
	if p, capped := b.wait(lowRate / 2); p != 0 || capped {
		t.Fatalf("pause %v capped %v", p, capped)
	}
}

func TestBudgetAtCapPausesProportionally(t *testing.T) {
	b, _ := newTestBudget()
	b.wait(lowRate) // ведро пусто
	if p, capped := b.wait(lowRate / 2); p != 500*time.Millisecond || capped {
		t.Fatalf("half a second of budget: pause %v capped %v", p, capped)
	}
	if b.Stalls() != 0 {
		t.Fatal("proportional pause counted as stall")
	}
}

func TestBudgetOverMaxPauseCapsAndCountsStall(t *testing.T) {
	b, c := newTestBudget()
	b.wait(lowRate)
	p, capped := b.wait(3 * lowRate) // нужно 3 с
	if p != time.Second || !capped || b.Stalls() != 1 {
		t.Fatalf("pause %v capped %v stalls %d", p, capped, b.Stalls())
	}
	// Долг сверх потолка прощён: через секунду ведро снова на нуле, а не в минусе на 2 с.
	c.advance(time.Second)
	if p, capped := b.wait(lowRate / 10); p != 100*time.Millisecond || capped {
		t.Fatalf("after capped pause: %v %v", p, capped)
	}
}

func TestBudgetRefillsButNoUnlimitedBurst(t *testing.T) {
	b, c := newTestBudget()
	b.wait(lowRate)
	c.advance(time.Hour)
	if p, _ := b.wait(lowRate); p != 0 {
		t.Fatalf("idle must refill one second of budget, pause %v", p)
	}
	// Час тишины не превращается в час бюджета: вторая секунда уже ждёт.
	if p, capped := b.wait(lowRate); p != time.Second || capped {
		t.Fatalf("burst beyond one second: pause %v capped %v", p, capped)
	}
}

func TestBudgetZeroNegativeAndClockSkew(t *testing.T) {
	b, c := newTestBudget()
	for _, n := range []int{0, -1, -1 << 30} {
		if p, capped := b.wait(n); p != 0 || capped {
			t.Fatalf("wait(%d) = %v %v", n, p, capped)
		}
	}
	if b.tokens != lowRate || b.Stalls() != 0 {
		t.Fatalf("state changed: tokens %v stalls %d", b.tokens, b.Stalls())
	}
	b.wait(lowRate)
	c.advance(-time.Hour) // часы ушли назад — бюджет не прибавляется
	if p, _ := b.wait(lowRate / 2); p != 500*time.Millisecond {
		t.Fatalf("clock going back refilled budget: pause %v", p)
	}
}

func TestBudgetUnconfiguredProfileUsesLow(t *testing.T) {
	b := newBandwidthBudget(Profile{Name: "nobudget"}, (&fakeClock{}).now)
	if b.rate != lowRate || b.maxPause != time.Second {
		t.Fatalf("profile without budget: rate %v max pause %v", b.rate, b.maxPause)
	}
}
