//go:build screen

package screen

import (
	"errors"
	"testing"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// splitFrame нарезает кадр так же, как захватчик (screenframe.Split), и оборачивает
// части в wireFrame.
func splitFrame(t *testing.T, seq uint32, key bool, ntiles, tileBytes int) []wireFrame {
	t.Helper()
	var tiles []screenframe.Tile
	for i := 0; i < ntiles; i++ {
		tiles = append(tiles, screenframe.Tile{X: uint16(i * 64), W: 64, H: 64, JPEG: make([]byte, tileBytes)})
	}
	raw, err := screenframe.Split(seq, key, 640, 480, tiles, screenframe.MaxPartBytes)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]wireFrame, len(raw))
	for i, p := range raw {
		out[i] = wireFrame{Seq: seq, Keyframe: key, Last: i == len(raw)-1, Payload: p}
	}
	return out
}

func TestCheckPartsAcceptsWholeFrames(t *testing.T) {
	one := splitFrame(t, 1, true, 3, 1000)
	if len(one) != 1 {
		t.Fatalf("expected one part, got %d", len(one))
	}
	if err := checkParts(one); err != nil {
		t.Fatalf("single-part frame: %v", err)
	}
	many := splitFrame(t, 2, false, 6, 100<<10) // 600 KiB → 3 части
	if len(many) < 3 {
		t.Fatalf("expected several parts, got %d", len(many))
	}
	if err := checkParts(many); err != nil {
		t.Fatalf("multi-part frame: %v", err)
	}
}

func TestCheckPartsRejects(t *testing.T) {
	cases := map[string]func() []wireFrame{
		"no parts": func() []wireFrame { return nil },
		"part over 256 KiB": func() []wireFrame {
			f := splitFrame(t, 1, true, 1, 10)
			f[0].Payload = append(f[0].Payload, make([]byte, screenframe.MaxPartBytes)...)
			return f
		},
		"last_part missing": func() []wireFrame {
			f := splitFrame(t, 1, true, 6, 100<<10)
			f[len(f)-1].Last = false
			return f
		},
		"last_part on a middle part": func() []wireFrame {
			f := splitFrame(t, 1, true, 6, 100<<10)
			f[0].Last = true
			return f
		},
		"no RSF1 magic": func() []wireFrame {
			f := splitFrame(t, 1, true, 1, 10)
			f[0].Payload[0] = 'X'
			return f
		},
		"truncated header": func() []wireFrame {
			f := splitFrame(t, 1, true, 1, 10)
			f[0].Payload = f[0].Payload[:screenframe.HeaderLen-1]
			return f
		},
		"seq differs between parts": func() []wireFrame {
			f := splitFrame(t, 1, true, 6, 100<<10)
			f[1].Seq = 2
			return f
		},
		"framing seq differs from RSF1 header": func() []wireFrame {
			f := splitFrame(t, 1, true, 1, 10)
			f[0].Seq = 9
			return f
		},
		"keyframe flag differs between parts": func() []wireFrame {
			f := splitFrame(t, 1, true, 6, 100<<10)
			f[1].Keyframe = false
			return f
		},
	}
	for name, build := range cases {
		if err := checkParts(build()); !errors.Is(err, errBadFrame) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
