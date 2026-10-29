package scripts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
	pb "github.com/Floodww/RoutineOps/proto"
	"github.com/robfig/cron/v3"
)

const fetchTimeout = 30 * time.Second

// policyRunner исполняет одну политику (seam для тестов).
type policyRunner interface {
	Run(ctx context.Context, p *pb.ScriptPolicy, trigger pb.ScriptTrigger)
}

// Manager поллит эффективный набор скрипт-политик и исполняет их по триггерам:
// SCHEDULE (cron), EVENT (события ОС), ON_CONNECT (установка стрима).
type Manager struct {
	interval time.Duration
	log      *slog.Logger
	fetch    func(ctx context.Context, knownVersion int64) (*pb.FetchScriptPoliciesResponse, error)
	runner   policyRunner
	dedup    *dedupSet

	mu               sync.Mutex
	baseCtx          context.Context
	version          int64
	policies         []*pb.ScriptPolicy
	cron             *cron.Cron
	cronIDs          []cron.EntryID
	ready            bool // true после первого успешного поллинга
	pendingOnConnect bool // OnConnect вызван до готовности — отложить
	// running — политики, чей прогон ещё не завершён. Без этого «*/1 * * * *» на
	// скрипте, работающем дольше минуты, плодит бесконечно накладывающиеся прогоны
	// (cron.New() без SkipIfStillRunning всё равно бы не помог: launch детачит горутину).
	running map[string]bool
}

// NewManager собирает Manager с боевыми зависимостями: FetchScriptPolicies через
// dialer, исполнение через Runner (отчёт в outbox). dedupPath — файл дедупа
// on_connect-запусков (""=только память).
func NewManager(dialer *transport.Dialer, enqueue EnqueueFunc, interval time.Duration, dedupPath string, log *slog.Logger) *Manager {
	return &Manager{
		interval: interval,
		log:      log,
		runner:   NewRunner(enqueue, log),
		dedup:    loadDedupSet(dedupPath),
		fetch: func(ctx context.Context, knownVersion int64) (*pb.FetchScriptPoliciesResponse, error) {
			conn, err := dialer.Dial()
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
			return pb.NewAgentServiceClient(conn).FetchScriptPolicies(ctx,
				&pb.FetchScriptPoliciesRequest{KnownVersion: knownVersion})
		},
	}
}

// Run запускает поллинг и cron, блокирует до отмены ctx.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.baseCtx = ctx
	m.cron = cron.New()
	m.cron.Start()
	m.mu.Unlock()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.poll(ctx) // подтянуть набор сразу на старте

	// Помечаем готовность и запускаем отложенный OnConnect, если он пришёл раньше.
	m.mu.Lock()
	m.ready = true
	pending := m.pendingOnConnect
	m.pendingOnConnect = false
	m.mu.Unlock()
	if pending {
		m.OnConnect()
	}

	for {
		select {
		case <-ctx.Done():
			m.cron.Stop()
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *Manager) poll(ctx context.Context) {
	m.mu.Lock()
	known := m.version
	m.mu.Unlock()

	resp, err := m.fetch(ctx, known)
	if err != nil {
		m.log.Error("scripts: FetchScriptPolicies", slog.Any("error", err))
		return
	}
	// Пропускаем, если сервер сказал unchanged ИЛИ версия не изменилась (защита от
	// лишних пере-применений, если сервер не выставил флаг — напр. при пустом наборе).
	if resp.GetUnchanged() || resp.GetVersion() == known {
		return
	}
	m.apply(resp.GetPolicies(), resp.GetVersion())
}

// apply фиксирует новый набор и перестраивает cron-расписание под SCHEDULE-политики.
func (m *Manager) apply(policies []*pb.ScriptPolicy, version int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.policies = policies
	m.version = version

	// Пересобираем cron: снимаем старые записи, добавляем актуальные SCHEDULE.
	for _, id := range m.cronIDs {
		m.cron.Remove(id)
	}
	m.cronIDs = m.cronIDs[:0]
	scheduled := 0
	for _, p := range policies {
		if p.GetTrigger() != pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE {
			continue
		}
		pid := p.GetPolicyId()
		id, err := m.cron.AddFunc(p.GetCron(), func() {
			m.runByID(pid, pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
		})
		if err != nil {
			m.log.Error("scripts: некорректное cron-выражение",
				slog.String("policy_id", pid), slog.String("cron", p.GetCron()), slog.Any("error", err))
			continue
		}
		m.cronIDs = append(m.cronIDs, id)
		scheduled++
	}
	m.log.Info("scripts: политики обновлены",
		slog.Int("total", len(policies)), slog.Int("scheduled", scheduled), slog.Int64("version", version))
}

// OnConnect исполняет on_connect-политики с дедупом по policy_id+version.
// Вызывается транспортом при установке Connect-стрима.
// Если первый поллинг ещё не завершился — запрос откладывается до его окончания.
func (m *Manager) OnConnect() {
	m.mu.Lock()
	if !m.ready {
		m.pendingOnConnect = true
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	for _, p := range m.snapshot(pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT, 0) {
		key := fmt.Sprintf("%s@%d", p.GetPolicyId(), p.GetUpdatedAt())
		if !m.dedup.markIfNew(key) {
			continue // уже выполняли эту версию — реконнект не перезапускает
		}
		m.launch(p, pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT)
	}
}

// OnEvent исполняет EVENT-политики, подписанные на событие evt (без дедупа —
// событие может повторяться).
func (m *Manager) OnEvent(evt pb.ScriptEventType) {
	for _, p := range m.snapshot(pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT, evt) {
		m.launch(p, pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT)
	}
}

// runByID исполняет политику по id (cron хранит id, читает актуальную версию).
func (m *Manager) runByID(policyID string, trigger pb.ScriptTrigger) {
	m.mu.Lock()
	var found *pb.ScriptPolicy
	for _, p := range m.policies {
		if p.GetPolicyId() == policyID {
			found = p
			break
		}
	}
	m.mu.Unlock()
	if found != nil {
		m.launch(found, trigger)
	}
}

// snapshot отдаёт копию среза политик с нужным триггером (и событием для EVENT).
func (m *Manager) snapshot(trigger pb.ScriptTrigger, evt pb.ScriptEventType) []*pb.ScriptPolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*pb.ScriptPolicy
	for _, p := range m.policies {
		if p.GetTrigger() != trigger {
			continue
		}
		if trigger == pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT && p.GetEventTrigger() != evt {
			continue
		}
		out = append(out, p)
	}
	return out
}

// launch исполняет политику в отдельной горутине (не блокирует cron/хуки).
// Прогон, начатый пока предыдущий ещё идёт, пропускается: иначе долгий скрипт на
// частом расписании множит параллельные копии самого себя и дубли результатов.
func (m *Manager) launch(p *pb.ScriptPolicy, trigger pb.ScriptTrigger) {
	policyID := p.GetPolicyId()

	m.mu.Lock()
	ctx := m.baseCtx
	if m.running[policyID] {
		m.mu.Unlock()
		m.log.Warn("scripts: предыдущий прогон ещё идёт, пропуск",
			slog.String("policy_id", policyID), slog.String("trigger", trigger.String()))
		return
	}
	if m.running == nil {
		m.running = make(map[string]bool)
	}
	m.running[policyID] = true
	m.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, policyID)
			m.mu.Unlock()
		}()
		m.runner.Run(ctx, p, trigger)
	}()
}
