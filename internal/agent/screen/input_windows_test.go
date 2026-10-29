//go:build screen && windows

package screen

import (
	"testing"
	"unsafe"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestInputRecLayoutMatchesWin64(t *testing.T) {
	if unsafe.Sizeof(inputRec{}) != 40 {
		t.Fatalf("INPUT is %d bytes, Win64 expects 40", unsafe.Sizeof(inputRec{}))
	}
	var r inputRec
	if off := uintptr(unsafe.Pointer(&r.u[0])) - uintptr(unsafe.Pointer(&r)); off != 8 {
		t.Fatalf("union offset %d, want 8", off)
	}
}

func TestAbsPosScalesFrameToFullRange(t *testing.T) {
	x, y, ok := absPos(&pb.InputEvent{X: 0, Y: 0}, 1280, 720)
	if !ok || x != 0 || y != 0 {
		t.Fatalf("origin %d,%d %v", x, y, ok)
	}
	x, y, _ = absPos(&pb.InputEvent{X: 1279, Y: 719}, 1280, 720)
	if x != 65535 || y != 65535 {
		t.Fatalf("far corner %d,%d", x, y)
	}
	x, _, _ = absPos(&pb.InputEvent{X: 5000, Y: 5}, 1280, 720) // за краем — прижимается
	if x != 65535 {
		t.Fatalf("clamp %d", x)
	}
	if _, _, ok := absPos(&pb.InputEvent{}, 0, 0); ok {
		t.Fatal("no frame size → no position")
	}
}

func TestScanOfPhysicalKeys(t *testing.T) {
	for code, want := range map[string]struct {
		sc  uint16
		ext bool
	}{
		"KeyA": {0x1E, false}, "KeyQ": {0x10, false}, "KeyM": {0x32, false},
		"Digit1": {0x02, false}, "Digit0": {0x0B, false}, "F1": {0x3B, false}, "F10": {0x44, false},
		"F12": {0x58, false}, "Enter": {0x1C, false}, "NumpadEnter": {0x1C, true},
		"ArrowUp": {0x48, true}, "ControlRight": {0x1D, true}, "Delete": {0x53, true},
	} {
		sc, ext, ok := scanOf(code)
		if !ok || sc != want.sc || ext != want.ext {
			t.Errorf("%s: %#x ext=%v ok=%v", code, sc, ext, ok)
		}
	}
	if _, _, ok := scanOf("Unknown"); ok {
		t.Fatal("unknown key must be skipped, not mapped")
	}
}
