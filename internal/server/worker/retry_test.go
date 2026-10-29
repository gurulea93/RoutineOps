package worker

import (
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

// TestDeliverRetryDelay_CappedForDeliver: backoff доставки задач линейный и с
// потолком 30с (БАГ 5) — lock не должен ждать ретрая часами.
func TestDeliverRetryDelay_CappedForDeliver(t *testing.T) {
	task := asynq.NewTask(TypeDeliverTask, nil)

	cases := map[int]time.Duration{
		1:   3 * time.Second,
		2:   6 * time.Second,
		10:  30 * time.Second, // 30с дотянулись до потолка
		100: 30 * time.Second, // и не растёт дальше
	}
	for n, want := range cases {
		if got := deliverRetryDelay(n, nil, task); got != want {
			t.Errorf("deliverRetryDelay(n=%d) = %s, хотим %s", n, got, want)
		}
	}
}

// TestDeliverRetryDelay_OtherTypesUseDefault: для прочих типов задач остаётся
// дефолтный экспоненциальный backoff asynq (растёт с n).
func TestDeliverRetryDelay_OtherTypesUseDefault(t *testing.T) {
	other := asynq.NewTask("some:other", nil)
	d1 := deliverRetryDelay(1, nil, other)
	d5 := deliverRetryDelay(5, nil, other)
	if d5 <= d1 {
		t.Errorf("дефолтный backoff должен расти: n=1 → %s, n=5 → %s", d1, d5)
	}
	// и он заведомо длиннее нашего потолка для доставки на большом n
	if d5 <= 30*time.Second {
		t.Errorf("ожидали, что дефолтный backoff на n=5 (%s) длиннее потолка доставки 30с", d5)
	}
}
