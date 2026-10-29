package scripts

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

// waitGoroutines форсирует GC и ждёт, пока число горутин опустится до <= want
// (или вернёт последнее значение по истечении дедлайна). Завершение cron после
// Stop асинхронно, поэтому нужен опрос, а не мгновенный замер.
func waitGoroutines(want int) int {
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

// TestRunNoGoroutineLeakAtScale симулирует множество параллельных агентов: N
// Script-менеджеров крутят свой Run (каждый поднимает cron-горутину), затем все
// отменяются. После отмены число горутин должно вернуться к базовому — иначе в
// конвейере (cron.Stop, тикер) утечка. Прямо адресует «могут быть ещё горутин-
// лики» из плана Phase 7.
func TestRunNoGoroutineLeakAtScale(t *testing.T) {
	const agents = 30

	runtime.GC()
	runtime.GC()
	base := runtime.NumGoroutine() // базовый замер до старта

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		m := &Manager{
			interval: 5 * time.Millisecond,
			log:      discardLog(),
			runner:   &fakeRunner{ch: make(chan runCall, 1)},
			dedup:    loadDedupSet(""),
			fetch: func(context.Context, int64) (*pb.FetchScriptPoliciesResponse, error) {
				// Пустой набор: cron без задач, никто не дёргает runner.
				return &pb.FetchScriptPoliciesResponse{Version: 0}, nil
			},
		}
		wg.Add(1)
		go func() { defer wg.Done(); m.Run(ctx) }()
	}

	// Дать менеджерам выйти на режим (несколько тиков, cron поднят).
	time.Sleep(50 * time.Millisecond)
	if running := runtime.NumGoroutine(); running <= base {
		t.Fatalf("ожидали рост горутин под нагрузкой (база %d, сейчас %d)", base, running)
	}

	cancel()
	wg.Wait()

	// После отмены и cron.Stop лишних горутин быть не должно. Порог небольшой —
	// на джиттер runtime/финализаторы, но линейный лик в N=30 он поймает.
	if end := waitGoroutines(base + 2); end > base+2 {
		t.Errorf("утечка горутин: база %d, после отмены %d агентов осталось %d", base, agents, end)
	}
}
