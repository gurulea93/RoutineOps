package gateway_test

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestConnect_NoGoroutineLeakAtScale(t *testing.T) {
	const agents = 20
	db := newDB(t)

	settle := func(want int) int {
		deadline := time.Now().Add(3 * time.Second)
		for {
			runtime.GC()
			n := runtime.NumGoroutine()
			if n <= want || time.Now().After(deadline) {
				return n
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	runtime.GC()
	runtime.GC()
	base := runtime.NumGoroutine()

	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		cn := fmt.Sprintf("load-agent-%d", i)
		certCtx, fingerprint := makeCertCtx(t, cn)
		registerDevice(t, db, cn, fingerprint)

		stream := &mockStream{
			ctx: certCtx,
			msgs: []*pb.HeartbeatRequest{
				{IpAddress: "192.0.2.1"},
				{IpAddress: "192.0.2.1"},
			},
			// после двух heartbeat — EOF, Connect вернётся
		}
		gw := newGW(t, db)

		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = gw.Connect(stream)
		}()
	}

	wg.Wait()

	after := settle(base + 3) // +3 запас на служебные горутины Go runtime
	if after > base+3 {
		t.Errorf("горутин после %d Connect: %d (база %d) — возможная утечка",
			agents, after, base)
	}
}
