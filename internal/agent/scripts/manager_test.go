package scripts

import (
	"context"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
	"github.com/robfig/cron/v3"
)

type runCall struct {
	policyID string
	trigger  pb.ScriptTrigger
}

type fakeRunner struct{ ch chan runCall }

func (f *fakeRunner) Run(_ context.Context, p *pb.ScriptPolicy, tr pb.ScriptTrigger) {
	f.ch <- runCall{p.GetPolicyId(), tr}
}

func newTestManager(fr *fakeRunner, dedupPath string) *Manager {
	return &Manager{
		log:     discardLog(),
		runner:  fr,
		dedup:   loadDedupSet(dedupPath),
		cron:    cron.New(),
		baseCtx: context.Background(),
		ready:   true, // тесты обходят Run(), поэтому выставляем вручную
	}
}

// waitCall ждёт один запуск (launch асинхронный) или падает по таймауту.
func waitCall(t *testing.T, fr *fakeRunner) runCall {
	t.Helper()
	select {
	case c := <-fr.ch:
		return c
	case <-time.After(time.Second):
		t.Fatal("ожидали запуск политики, не дождались")
		return runCall{}
	}
}

func noCall(t *testing.T, fr *fakeRunner) {
	t.Helper()
	select {
	case c := <-fr.ch:
		t.Fatalf("неожиданный запуск: %+v", c)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestOnConnectDedupByVersion(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "") // дедуп в памяти

	onc := &pb.ScriptPolicy{
		PolicyId: "p1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT, UpdatedAt: 10,
	}
	m.apply([]*pb.ScriptPolicy{onc}, 10)

	m.OnConnect()
	if c := waitCall(t, fr); c.policyID != "p1" || c.trigger != pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT {
		t.Fatalf("первый on_connect: %+v", c)
	}
	// Повторный коннект той же версии — без перезапуска.
	m.OnConnect()
	noCall(t, fr)

	// Bump версии политики → новый ключ дедупа → запуск.
	onc2 := &pb.ScriptPolicy{
		PolicyId: "p1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT, UpdatedAt: 20,
	}
	m.apply([]*pb.ScriptPolicy{onc2}, 20)
	m.OnConnect()
	if c := waitCall(t, fr); c.policyID != "p1" {
		t.Fatalf("после bump версии: %+v", c)
	}
}

func TestOnEventFiltersByType(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "")
	m.apply([]*pb.ScriptPolicy{
		{PolicyId: "login1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT, EventTrigger: pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN},
		{PolicyId: "logout1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT, EventTrigger: pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGOUT},
		{PolicyId: "sched1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE, Cron: "* * * * *"},
	}, 1)

	m.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN)
	if c := waitCall(t, fr); c.policyID != "login1" {
		t.Fatalf("на LOGIN ожидали login1, got %+v", c)
	}
	noCall(t, fr) // logout1/sched1 не должны сработать на LOGIN

	// Событие без подписчиков — ничего.
	m.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_NETWORK_CHANGE)
	noCall(t, fr)
}

func TestApplyConfiguresCronAndSkipsInvalid(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "")
	m.apply([]*pb.ScriptPolicy{
		{PolicyId: "ok", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE, Cron: "*/5 * * * *"},
		{PolicyId: "bad", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE, Cron: "не-крон"},
	}, 1)
	if len(m.cronIDs) != 1 {
		t.Fatalf("ожидали 1 валидную cron-запись, got %d", len(m.cronIDs))
	}
	// Повторный apply снимает старые записи (нет накопления).
	m.apply([]*pb.ScriptPolicy{
		{PolicyId: "ok", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE, Cron: "*/5 * * * *"},
	}, 2)
	if len(m.cronIDs) != 1 {
		t.Fatalf("после повторного apply ожидали 1 запись, got %d", len(m.cronIDs))
	}
}

func TestPollUnchangedKeepsState(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "")

	calls := 0
	m.fetch = func(_ context.Context, known int64) (*pb.FetchScriptPoliciesResponse, error) {
		calls++
		if calls == 1 {
			return &pb.FetchScriptPoliciesResponse{
				Version:  5,
				Policies: []*pb.ScriptPolicy{{PolicyId: "e1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT, EventTrigger: pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN}},
			}, nil
		}
		// Второй poll: сервер видит ту же версию (агент прислал known=5).
		if known != 5 {
			t.Fatalf("второй poll прислал known=%d, ожидали 5", known)
		}
		return &pb.FetchScriptPoliciesResponse{Unchanged: true}, nil
	}

	m.poll(context.Background())
	if m.version != 5 || len(m.policies) != 1 {
		t.Fatalf("после первого poll version=%d policies=%d", m.version, len(m.policies))
	}
	m.poll(context.Background())
	if m.version != 5 || len(m.policies) != 1 {
		t.Fatalf("unchanged не должен сбрасывать состояние: version=%d policies=%d", m.version, len(m.policies))
	}
	// Набор сохранился — событие по нему срабатывает.
	m.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN)
	if c := waitCall(t, fr); c.policyID != "e1" {
		t.Fatalf("ожидали e1 по LOGIN, got %+v", c)
	}
}
