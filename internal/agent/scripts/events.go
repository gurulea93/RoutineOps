package scripts

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

// EventSink принимает события ОС (обычно *Manager.OnEvent).
type EventSink interface {
	OnEvent(evt pb.ScriptEventType)
}

// EventWatcher опросом обнаруживает события ОС для EVENT-политик: вход/выход
// пользователя (смена консольного пользователя) и смену сети (смена основного IP).
// Polling, без системных подписок — задержка обнаружения в секунды приемлема
// (как Security Monitor, CONTEXT §4).
type EventWatcher struct {
	interval    time.Duration
	log         *slog.Logger
	sink        EventSink
	ipFunc      func() string
	consoleUser func() string

	lastUser string
	lastIP   string
}

// NewEventWatcher creates a watcher. ipFunc — текущий основной IP (collector.LocalIP).
func NewEventWatcher(interval time.Duration, sink EventSink, ipFunc func() string, log *slog.Logger) *EventWatcher {
	return &EventWatcher{
		interval:    interval,
		log:         log,
		sink:        sink,
		ipFunc:      ipFunc,
		consoleUser: osConsoleUser,
	}
}

// Run опрашивает состояние и эмитит события, пока ctx жив. Базовое состояние при
// старте берётся без эмита (агент не считает «уже вошедшего» пользователя за вход).
func (w *EventWatcher) Run(ctx context.Context) {
	w.lastUser = w.consoleUser()
	w.lastIP = w.ipFunc()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *EventWatcher) poll() {
	if u := w.consoleUser(); u != w.lastUser {
		// Переход: сначала выход прежнего (если был), затем вход нового (если есть).
		if w.lastUser != "" {
			w.log.Info("scripts: событие logout", slog.String("user", w.lastUser))
			w.sink.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGOUT)
		}
		if u != "" {
			w.log.Info("scripts: событие login", slog.String("user", u))
			w.sink.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN)
		}
		w.lastUser = u
	}
	if ip := w.ipFunc(); ip != w.lastIP {
		w.log.Info("scripts: событие network_change", slog.String("ip", ip))
		w.sink.OnEvent(pb.ScriptEventType_SCRIPT_EVENT_TYPE_NETWORK_CHANGE)
		w.lastIP = ip
	}
}
