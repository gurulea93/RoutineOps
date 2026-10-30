//go:build screen

package screen

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	pb "github.com/Floodww/RoutineOps/proto"
)

// SessionState — фаза сеанса на стороне агента. Переходы только вперёд:
// requested → active → ended либо requested → ended (отказ до первого кадра).
type SessionState int

const (
	StateRequested SessionState = iota // приглашение принято, захватчик ещё не готов
	StateActive                        // сервер ответил SessionAccepted, кадры идут
	StateEnded                         // терминально; причина зафиксирована один раз
)

func (s SessionState) String() string {
	if names := [...]string{"requested", "active", "ended"}; s >= 0 && int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// session — один сеанс. Поля под mu; cmd неизменяем после создания.
type session struct {
	id     string
	cmd    *pb.ScreenSessionCommand
	ctx    context.Context
	cancel context.CancelFunc

	// onSignal — куда readPipe отдаёт сигнал от worker'а (msgSignal); nil — сигналов не ждём.
	onSignal func([]byte)

	mu        sync.Mutex
	state     SessionState
	endReason EndReason
	endDetail string    // техническая причина (текст ошибки), без повода/e-mail оператора и экрана
	stats     wireStats // счётчики для SessionStats, приходят от захватчика

	// ready закрывается, когда исход старта известен: захват пошёл (run зовёт signalReady)
	// либо сеанс закрыт раньше (end). На нём ждёт StartSession, который обязан ответить
	// приглашению кодом отказа синхронно.
	ready     chan struct{}
	readyOnce sync.Once

	// inputOn — управление ещё у оператора. Выставляется приглашением и только снимается
	// (возврат управления = сужение), обратно не включается.
	inputOn atomic.Bool

	// display — монитор, который агент уже просил показывать (0 — основной, как при старте).
	display atomic.Int32
}

func newSession(parent context.Context, cmd *pb.ScreenSessionCommand) *session {
	ctx, cancel := context.WithCancel(parent)
	s := &session{id: cmd.GetSessionId(), cmd: cmd, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	s.inputOn.Store(cmd.GetAllowInput())
	return s
}

// activate — requested → active. Ложь, если сеанс уже не ждёт старта.
func (s *session) activate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRequested {
		return false
	}
	s.state = StateActive
	return true
}

// end — переход в ended из любой нетерминальной фазы. Первые причина и detail побеждают;
// возвращает итоговую причину и признак того, что закрыл именно этот вызов.
func (s *session) end(reason EndReason, detail string) (EndReason, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateEnded {
		return s.endReason, false
	}
	s.state, s.endReason, s.endDetail = StateEnded, reason, detail
	s.cancel()
	s.signalReady()
	return reason, true
}

// signalReady — исход старта известен. Повторные вызовы безвредны.
func (s *session) signalReady() { s.readyOnce.Do(func() { close(s.ready) }) }

// reason — причина завершения (пусто, пока сеанс не закрыт).
func (s *session) reason() EndReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endReason
}

// snapshot — фаза, счётчики и detail завершения (пусто, пока сеанс не закрыт).
func (s *session) snapshot() (SessionState, wireStats, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.stats, s.endDetail
}

func (s *session) setStats(st wireStats) { s.mu.Lock(); s.stats = st; s.mu.Unlock() }
