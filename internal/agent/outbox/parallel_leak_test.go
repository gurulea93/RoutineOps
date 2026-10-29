package outbox

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestParallelQueuesNoLeak симулирует множество агентов: N независимых очередей,
// каждая со своим фоновым Run, параллельно принимают и сливают события. После
// отмены общего контекста все Run-горутины обязаны завершиться — проверяем, что
// число горутин возвращается к базовому (нет утечки в durable-конвейере при
// масштабе, дополняет одно-очередной soak_test.go).
func TestParallelQueuesNoLeak(t *testing.T) {
	const agents = 20

	settle := func(want int) int {
		deadline := time.Now().Add(2 * time.Second)
		for {
			runtime.GC()
			n := runtime.NumGoroutine()
			if n <= want || time.Now().After(deadline) {
				return n
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	runtime.GC()
	runtime.GC()
	base := runtime.NumGoroutine()

	var delivered int64
	dispatch := func(_ context.Context, _ string, _ []byte) error {
		atomic.AddInt64(&delivered, 1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	var runWG, loadWG sync.WaitGroup
	payload := make([]byte, 128)

	for i := 0; i < agents; i++ {
		q, err := New(t.TempDir(), 500, 2*time.Millisecond, discardLog(), dispatch)
		if err != nil {
			t.Fatal(err)
		}
		runWG.Add(1)
		go func() { defer runWG.Done(); q.Run(ctx) }()

		// Нагрузочный продюсер на каждую очередь, пока жив контекст.
		loadWG.Add(1)
		go func() {
			defer loadWG.Done()
			for ctx.Err() == nil {
				_ = q.Enqueue(KindSecurity, payload)
				time.Sleep(time.Millisecond)
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	if running := runtime.NumGoroutine(); running <= base {
		t.Fatalf("ожидали рост горутин под нагрузкой (база %d, сейчас %d)", base, running)
	}

	cancel()
	loadWG.Wait()
	runWG.Wait()

	if atomic.LoadInt64(&delivered) == 0 {
		t.Fatal("ни одно событие не доставлено — нагрузка не сработала")
	}
	if end := settle(base + 2); end > base+2 {
		t.Errorf("утечка горутин: база %d, после отмены %d очередей осталось %d", base, agents, end)
	}
}
