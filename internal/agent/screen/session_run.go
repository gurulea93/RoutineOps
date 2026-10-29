//go:build screen

package screen

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

const (
	// maxSessionCeiling — потолок длительности сеанса на агенте (max_duration_sec = 0 или больше).
	maxSessionCeiling = time.Hour
	// helloTimeout — сколько ждём первого сообщения захватчика. На Retina первый снимок
	// делается ДО подключения к каналу (command/executor.go: до 20 с), поэтому ровно 20 с.
	helloTimeout = 20 * time.Second
)

// runDeps — всё внешнее, что нужно run. Функциями, а не конкретными типами: тело сеанса
// проверяется без процессов, без gRPC и без файловой очереди.
type runDeps struct {
	launch func(cfg wireConfig) (*workerProc, EndReason, error)
	dial   func(s *session, h wireHello) (*sessionStream, EndReason, error)
	sink   eventSink
	log    *slog.Logger
	now    func() time.Time

	// Переопределения для тестов; ноль — значения по умолчанию.
	ceiling time.Duration
	hello   time.Duration
}

// run — весь жизненный цикл сеанса: проверки приглашения → захватчик → hello → стрим →
// циклы → finishSession. Блокируется до конца сеанса (его вызывает Manager в своей
// горутине). Любой выход, ранний или поздний, проходит через finishSession: он пишет
// терминальное событие, гасит захватчика (и с ним плашку) и закрывает стрим.
func (d *runDeps) run(s *session) {
	var (
		w   *workerProc
		st  *sessionStream
		ctl *ctlWriter
		wg  sync.WaitGroup
	)
	defer func() {
		if ctl != nil { // вежливая остановка; без неё захватчик всё равно выйдет по EOF stdin
			if b, err := json.Marshal(wireStop{Reason: string(s.reason())}); err == nil {
				_ = ctl.send(msgStop, b)
			}
		}
		finishSession(s, st, w, d.sink, d.log, d.now)
		wg.Wait() // читающие циклы выходят: ctx отменён, стрим и пайп закрыты
		s.signalReady()
	}()

	// Что агент не умеет, отказываем кодом ДО запуска захвата, а не понижаем молча (§4).
	switch cmd := s.cmd; {
	case cmd.GetAllowInput():
		s.end(ReasonInputUnavailable, "input injection is not available in this build")
		return
	case cmd.GetConsentRequired():
		s.end(ReasonConsentUnavailable, "consent dialog is not available in this build")
		return
	}

	dur := cmp.Or(d.ceiling, maxSessionCeiling)
	if n := time.Duration(s.cmd.GetMaxDurationSec()) * time.Second; n > 0 && n < dur {
		dur = n
	}

	var reason EndReason
	var err error
	w, reason, err = d.launch(wireConfig{
		SessionID: s.id, Profile: s.cmd.GetProfile(), Operator: s.cmd.GetOperatorName(),
		Reason: s.cmd.GetReason(), MaxDurationSec: int(dur / time.Second),
	})
	if reason != "" || err != nil {
		w = nil
		s.end(cmp.Or(reason, ReasonWorkerLost), errText(err))
		return
	}

	hello, ok := d.readHello(s, w, cmp.Or(d.hello, helloTimeout))
	if !ok {
		return
	}
	st, _, err = d.dial(s, hello) // при отказе dialSession сам закрыл сеанс
	if err != nil {
		st = nil
		return
	}

	ctl = &ctlWriter{w: w.stdin}
	box := newLatestFrame(ctl.askKeyframe)
	prof, _ := ProfileByName(hello.Profile)
	budget := newBandwidthBudget(prof, nil)

	wg.Add(2)
	go func() { defer wg.Done(); readPipe(s.ctx, s, w.stdout, box, d.log) }()
	go func() { defer wg.Done(); loopDown(s.ctx, s, st, ctl, d.log) }()
	limit := time.AfterFunc(dur, func() { s.end(ReasonMaxDuration, "") })
	defer limit.Stop()

	s.signalReady() // захват пошёл: StartSession может ответить «принято»
	// Единственный писатель стрима — эта горутина; SessionEnd пишет finishSession после неё.
	sendLoop(s.ctx, s, box, budget, st, nil)
}

// readHello ждёт от захватчика первое сообщение. Ложь — сеанс уже закрыт с причиной.
func (d *runDeps) readHello(s *session, w *workerProc, timeout time.Duration) (wireHello, bool) {
	got := make(chan pipeMsg, 1)
	go func() {
		typ, body, err := readMsg(w.stdout)
		got <- pipeMsg{typ, body, err}
	}()
	var m pipeMsg
	select {
	case m = <-got:
	case <-s.ctx.Done():
		return wireHello{}, false
	case <-time.After(timeout):
		s.end(ReasonWorkerLost, fmt.Sprintf("no hello from worker within %s", timeout))
		return wireHello{}, false
	}
	if m.err != nil {
		s.end(ReasonWorkerLost, m.err.Error())
		return wireHello{}, false
	}
	switch m.typ {
	case msgHello:
		var h wireHello
		if err := json.Unmarshal(m.body, &h); err != nil || h.Width <= 0 || h.Height <= 0 {
			s.end(ReasonProtocolError, "bad worker hello")
			return wireHello{}, false
		}
		return h, true
	case msgEnd: // захватчик отказался сам: нет консоли, Wayland и т. п. — причина его
		var e wireEnd
		_ = json.Unmarshal(m.body, &e)
		s.end(EndReason(cmp.Or(e.Reason, string(ReasonWorkerLost))), e.Detail)
	default:
		s.end(ReasonProtocolError, fmt.Sprintf("first worker message has type %d, want hello", m.typ))
	}
	return wireHello{}, false
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Service — реализация command.ScreenSessioner поверх Manager.
type Service struct {
	mgr *Manager
}

// NewService собирает службу сеансов. parent — контекст службы агента.
func NewService(parent context.Context, d *runDeps, log *slog.Logger) *Service {
	return &Service{mgr: NewManager(parent, d.run, log)}
}

// StartSession принимает приглашение и отвечает КОДОМ: пусто и nil — захват пошёл;
// иначе код отказа (EndReason) и ошибка с пояснением. Ждёт не дольше ctx.
func (v *Service) StartSession(ctx context.Context, _ string, cmd *pb.ScreenSessionCommand) (string, error) {
	s, busy := v.mgr.Start(cmd)
	if s == nil {
		return string(busy), errors.New("another screen session is already running")
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		s.end(ReasonTransportLost, "start cancelled: "+ctx.Err().Error())
		return string(ReasonTransportLost), ctx.Err()
	}
	if st, _, detail := s.snapshot(); st == StateEnded {
		return string(s.reason()), errors.New(cmp.Or(detail, string(s.reason())))
	}
	return "", nil
}
