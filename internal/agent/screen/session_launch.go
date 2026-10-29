//go:build screen

package screen

import (
	"io"
	"time"
)

// Запуск процесса захвата (agent screen-worker) в графической сессии сотрудника.
// Реализация — по платформе (session_launch_linux.go / session_launch_other.go).
//
// Параметры сеанса едут ПЕРВЫМ сообщением по stdin (msgConfig), а не аргументами:
// командная строка процесса видна любому локальному пользователю через ps, а в ней
// оказались бы e-mail оператора и повод сеанса.

const msgConfig byte = 12 // служба → worker: параметры сеанса, строго первым сообщением

type wireConfig struct {
	SessionID      string `json:"session_id"`
	Profile        string `json:"profile"`
	Operator       string `json:"operator"`
	Reason         string `json:"reason"`
	MaxDurationSec int    `json:"max_duration_sec"`
	AllowInput     bool   `json:"allow_input,omitempty"`
}

// workerProc — запущенный захватчик и его концы канала (wire.go).
type workerProc struct {
	stdin  io.WriteCloser // служба → worker
	stdout io.ReadCloser  // worker → служба
	wait   func() error   // дождаться выхода процесса
	kill   func()         // убить, если не вышел сам
}

// stopTimeout — сколько ждём штатного выхода захватчика после закрытия его stdin.
const stopTimeout = 5 * time.Second

// close закрывает канал и дожидается выхода; не вышел за stopTimeout — убивает.
func (p *workerProc) close() error {
	_ = p.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- p.wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(stopTimeout):
		p.kill()
		return <-done
	}
}

// launchWorker запускает захватчик. Непустая причина — графической сессии, в которой
// можно показать плашку и снимать экран, нет (NO_CONSOLE_USER, WAYLAND_UNSUPPORTED,
// PLATFORM_UNSUPPORTED); err тогда поясняет детали. stderr — куда пишет лог процесса.
func launchWorker(exe string, cfg wireConfig, stderr io.Writer) (*workerProc, EndReason, error) {
	p, reason, err := launchPlatform(exe, stderr)
	if reason != "" || err != nil {
		return nil, reason, err
	}
	if err := writeJSONMsg(p.stdin, msgConfig, cfg); err != nil {
		_ = p.close()
		return nil, ReasonWorkerLost, err
	}
	return p, "", nil
}
