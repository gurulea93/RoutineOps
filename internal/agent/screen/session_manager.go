//go:build screen

package screen

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Manager держит правило «один интерактивный сеанс на устройство в каждый момент».
// Что именно делает сеанс (запуск захватчика, стрим, циклы), знает run: менеджер только
// выдаёт и забирает место, поэтому его можно проверить без процессов и без сервера.
type Manager struct {
	parent context.Context
	run    func(*session)
	log    *slog.Logger

	mu  sync.Mutex
	cur *session
	wg  sync.WaitGroup
}

// NewManager: parent — контекст службы (его отмена гасит все сеансы), run — тело сеанса.
// run обязан сам довести сеанс до finishSession; менеджер лишь страхует от паники.
func NewManager(parent context.Context, run func(*session), log *slog.Logger) *Manager {
	return &Manager{parent: parent, run: run, log: log}
}

// Start принимает приглашение. Занято — nil и ReasonSessionBusy: вторая картинка экрана
// сотрудника параллельно первой не нужна, а плашка и согласие рассчитаны на один сеанс.
// Принято — сеанс запущен в своей горутине, возвращается он сам и пустая причина.
func (m *Manager) Start(cmd *pb.ScreenSessionCommand) (*session, EndReason) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		return nil, ReasonSessionBusy
	}
	s := newSession(m.parent, cmd)
	m.cur = s
	m.wg.Add(1)
	go m.exec(s)
	return s, ""
}

// exec — жизнь одного сеанса в горутине. Место освобождается при любом выходе run, в том
// числе по панике: иначе одна паника навсегда заблокировала бы удалённый просмотр до
// перезапуска службы.
func (m *Manager) exec(s *session) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		if m.cur == s {
			m.cur = nil
		}
		m.mu.Unlock()
		s.cancel() // освободить ресурсы контекста, даже если run забыл закрыть сеанс
	}()
	defer func() {
		if r := recover(); r != nil {
			m.log.Error("screen: session run panicked", "session_id", s.id, "panic", fmt.Sprint(r))
			s.end(ReasonWorkerLost, truncRunes("panic: "+fmt.Sprint(r), maxStopDetail))
		}
	}()
	m.run(s)
}

// Busy — идёт ли сеанс сейчас.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

// Wait ждёт завершения всех запущенных сеансов (остановка службы, тесты).
func (m *Manager) Wait() { m.wg.Wait() }
