//go:build screen

package screen

import (
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestOrderMonitors(t *testing.T) {
	in := []monitorRect{{X: -1920, W: 1920, H: 1080}, {X: 0, W: 2560, H: 1440, Primary: true}, {X: 2560, W: 1920, H: 1080}}
	got := orderMonitors(in)
	if !got[0].Primary || got[1].X != -1920 || got[2].X != 2560 {
		t.Fatalf("order: %+v", got)
	}
	if in[0].Primary {
		t.Fatal("input mutated")
	}
}

func TestVirtualPos(t *testing.T) {
	// Два монитора 1920x1080 бок о бок: рабочий стол 3840 шириной с началом в 0.
	r := monitorRect{X: 1920, Y: 0, W: 1920, H: 1080}
	x, y, ok := virtualPos(r, 0, 0, 3840, 1080, 0, 0, 1920, 1080)
	if !ok || y != 0 || x < 32000 || x > 33000 { // 1920/3839 ≈ 0.5
		t.Fatalf("left edge: %d %d %v", x, y, ok)
	}
	x, _, _ = virtualPos(r, 0, 0, 3840, 1080, 1919, 0, 1920, 1080)
	if x != 65535 {
		t.Fatalf("right edge: %d", x)
	}
	// Монитор слева от основного: отрицательное начало рабочего стола.
	l := monitorRect{X: -1920, W: 1920, H: 1080}
	x, _, _ = virtualPos(l, -1920, 0, 3840, 1080, 0, 0, 1920, 1080)
	if x != 0 {
		t.Fatalf("negative origin: %d", x)
	}
	if _, _, ok := virtualPos(l, 0, 0, 1, 1, 0, 0, 10, 10); ok {
		t.Fatal("degenerate desktop accepted")
	}
}

func TestSplitProfileCode(t *testing.T) {
	in := &pb.ScreenScope{Displays: []int32{1, profileCodeBase + 1}, AllowInput: true}
	out, p := splitProfileCode(in)
	if p != "low" || len(out.GetDisplays()) != 1 || out.GetDisplays()[0] != 1 || !out.GetAllowInput() {
		t.Fatalf("got %v %q", out, p)
	}
	if len(in.GetDisplays()) != 2 {
		t.Fatal("input mutated")
	}
	same, p := splitProfileCode(&pb.ScreenScope{Displays: []int32{0}})
	if p != "" || len(same.GetDisplays()) != 1 {
		t.Fatal("plain scope changed")
	}
}
