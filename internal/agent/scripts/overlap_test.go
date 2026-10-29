package scripts

import (
	"context"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

// blockingRunner держит Run до закрытия release — имитирует скрипт, который идёт
// дольше периода расписания.
type blockingRunner struct {
	started chan string
	release chan struct{}
}

func (b *blockingRunner) Run(_ context.Context, p *pb.ScriptPolicy, _ pb.ScriptTrigger) {
	b.started <- p.GetPolicyId()
	<-b.release
}

// Долгий скрипт на частом расписании не должен накладываться сам на себя:
// второй launch, пока первый идёт, обязан быть пропущен.
func TestLaunchSkipsOverlappingRun(t *testing.T) {
	br := &blockingRunner{started: make(chan string, 4), release: make(chan struct{})}
	m := &Manager{log: discardLog(), runner: br, baseCtx: context.Background(), ready: true}
	policy := &pb.ScriptPolicy{PolicyId: "p1", Name: "long"}

	m.launch(policy, pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
	select {
	case <-br.started:
	case <-time.After(time.Second):
		t.Fatal("первый прогон не стартовал")
	}

	// Второй запуск, пока первый висит в Run.
	m.launch(policy, pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
	select {
	case id := <-br.started:
		t.Fatalf("перекрывающийся прогон %q не был пропущен", id)
	case <-time.After(200 * time.Millisecond):
	}

	// После завершения первого — можно снова. Ждём, пока launch снимет пометку
	// (снятие происходит в defer уже ПОСЛЕ возврата из Run).
	close(br.release)
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		busy := m.running[policy.GetPolicyId()]
		m.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("пометка о выполняющемся прогоне не снялась")
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.launch(policy, pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
	select {
	case <-br.started:
	case <-time.After(time.Second):
		t.Fatal("прогон после завершения предыдущего не стартовал")
	}
}
