//go:build screen

package screen

import (
	"strings"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

func ownedHub(control bool) *hub {
	h := newHub(640, 480)
	op := "user-1"
	h.setOwner("tenant-1", &SessionRow{OperatorID: &op, Control: control})
	return h
}

func TestBuildInputValidatesAndCounts(t *testing.T) {
	req := &inputBatchReq{FrameWidth: 1280, FrameHeight: 720, Events: []inputEventReq{
		{Kind: "mouse_move", X: 5, Y: 6},
		{Kind: "mouse_down", X: 5, Y: 6, Button: 0},
		{Kind: "mouse_up", X: 5, Y: 6, Button: 7}, // кнопки 7 нет — событие отбрасывается
		{Kind: "wheel", WheelY: 99},
		{Kind: "key_down", Code: "KeyA"},
		{Kind: "key_down", Code: ""},   // без кода — отбрасывается
		{Kind: "text", Text: "привет"}, // 6 рун
		{Kind: "teleport", X: 1},       // неизвестный вид — отбрасывается, а не рвёт пачку
	}}
	in, c, ok := buildInput(req)
	if !ok {
		t.Fatal("batch rejected")
	}
	if len(in.Events) != 5 {
		t.Fatalf("%d events kept, want 5: %+v", len(in.Events), in.Events)
	}
	if in.Events[2].WheelY != maxWheelClicks {
		t.Fatalf("wheel not clamped: %d", in.Events[2].WheelY)
	}
	if c.moves != 1 || c.clicks != 1 || c.keys != 1 || c.scrolls != 1 || c.texts != 6 {
		t.Fatalf("counts %+v", c)
	}
}

func TestBuildInputLimits(t *testing.T) {
	for name, req := range map[string]*inputBatchReq{
		"no frame size": {Events: []inputEventReq{{Kind: "mouse_move"}}},
		"huge frame":    {FrameWidth: 1 << 20, FrameHeight: 10},
		"too many":      {FrameWidth: 10, FrameHeight: 10, Events: make([]inputEventReq, maxInputEvents+1)},
		"too much text": {FrameWidth: 10, FrameHeight: 10, Events: textEvents(9, strings.Repeat("я", 256))},
	} {
		if _, _, ok := buildInput(req); ok {
			t.Errorf("%s: must be rejected", name)
		}
	}
	// Пустая пачка с release_all — легальна: так панель отпускает всё при потере фокуса.
	if _, _, ok := buildInput(&inputBatchReq{FrameWidth: 10, FrameHeight: 10, ReleaseAll: true}); !ok {
		t.Error("release_all without events must pass")
	}
}

func textEvents(n int, s string) []inputEventReq {
	out := make([]inputEventReq, n)
	for i := range out {
		out[i] = inputEventReq{Kind: "text", Text: s}
	}
	return out
}

func TestHubSendInputChecksOwnerAndControl(t *testing.T) {
	in := &pb.SessionInput{FrameWidth: 1, FrameHeight: 1}
	h := ownedHub(true)
	if v := h.sendInput("tenant-1", "user-2", in, inputCounts{}); v != inputNotYours {
		t.Fatalf("other operator: %v", v)
	}
	if v := h.sendInput("tenant-2", "user-1", in, inputCounts{}); v != inputNotYours {
		t.Fatalf("other tenant: %v", v)
	}
	if v := h.sendInput("tenant-1", "user-1", in, inputCounts{clicks: 2}); v != inputAccepted {
		t.Fatalf("owner: %v", v)
	}
	if got := <-h.input; got != in {
		t.Fatal("batch must reach the stream queue")
	}
	if c := h.takeCounts(); c.clicks != 2 {
		t.Fatalf("counts %+v", c)
	}
	if c := h.takeCounts(); !c.empty() {
		t.Fatal("counts must reset after take")
	}

	if v := ownedHub(false).sendInput("tenant-1", "user-1", in, inputCounts{}); v != inputNoControl {
		t.Fatalf("view-only session: %v", v)
	}
}

func TestHubGiveBackIsOneWay(t *testing.T) {
	h := ownedHub(true)
	in := &pb.SessionInput{FrameWidth: 1, FrameHeight: 1}
	_ = h.sendInput("tenant-1", "user-1", in, inputCounts{})
	if !h.giveBack() {
		t.Fatal("first return must succeed")
	}
	if len(h.input) != 0 {
		t.Fatal("queued input must be dropped on return")
	}
	select {
	case <-h.narrow:
	default:
		t.Fatal("stream must be told to narrow the scope")
	}
	if v := h.sendInput("tenant-1", "user-1", in, inputCounts{}); v != inputNoControl {
		t.Fatalf("input after return: %v", v)
	}
	if h.giveBack() {
		t.Fatal("second return must be a no-op")
	}
	if ownedHub(false).giveBack() {
		t.Fatal("nothing to return in a view-only session")
	}
}

func TestHubSendInputBacklogAndClosed(t *testing.T) {
	h := ownedHub(true)
	in := &pb.SessionInput{FrameWidth: 1, FrameHeight: 1}
	for i := 0; i < inputQueue; i++ {
		if v := h.sendInput("tenant-1", "user-1", in, inputCounts{}); v != inputAccepted {
			t.Fatalf("batch %d: %v", i, v)
		}
	}
	if v := h.sendInput("tenant-1", "user-1", in, inputCounts{}); v != inputBacklog {
		t.Fatalf("full queue: %v", v)
	}
	h.close("AGENT_GONE")
	if v := h.sendInput("tenant-1", "user-1", in, inputCounts{}); v != inputClosed {
		t.Fatalf("closed hub: %v", v)
	}
}
