package scripts

import (
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

type fakeSink struct{ events []pb.ScriptEventType }

func (f *fakeSink) OnEvent(e pb.ScriptEventType) { f.events = append(f.events, e) }

func newTestWatcher(sink *fakeSink, user, ip *string) *EventWatcher {
	return &EventWatcher{
		log:         discardLog(),
		sink:        sink,
		consoleUser: func() string { return *user },
		ipFunc:      func() string { return *ip },
		lastUser:    *user,
		lastIP:      *ip,
	}
}

func TestEventWatcherTransitions(t *testing.T) {
	sink := &fakeSink{}
	user, ip := "alice", "1.1.1.1"
	w := newTestWatcher(sink, &user, &ip)

	// Без изменений — тишина.
	w.poll()
	if len(sink.events) != 0 {
		t.Fatalf("без изменений события: %v", sink.events)
	}

	// Смена IP.
	ip = "2.2.2.2"
	w.poll()
	if len(sink.events) != 1 || sink.events[0] != pb.ScriptEventType_SCRIPT_EVENT_TYPE_NETWORK_CHANGE {
		t.Fatalf("ожидали NETWORK_CHANGE, got %v", sink.events)
	}

	// Логаут (alice -> "").
	sink.events = nil
	user = ""
	w.poll()
	if len(sink.events) != 1 || sink.events[0] != pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGOUT {
		t.Fatalf("ожидали LOGOUT, got %v", sink.events)
	}

	// Логин ("" -> bob).
	sink.events = nil
	user = "bob"
	w.poll()
	if len(sink.events) != 1 || sink.events[0] != pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN {
		t.Fatalf("ожидали LOGIN, got %v", sink.events)
	}

	// Прямая смена пользователя (bob -> carol) = LOGOUT + LOGIN.
	sink.events = nil
	user = "carol"
	w.poll()
	if len(sink.events) != 2 ||
		sink.events[0] != pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGOUT ||
		sink.events[1] != pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN {
		t.Fatalf("ожидали LOGOUT+LOGIN, got %v", sink.events)
	}
}
