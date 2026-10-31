//go:build screen

package screenframe

import (
	"bytes"
	"testing"
)

// Кадр VP8 больше части режется на куски и склеивается обратно байт в байт; флаг VP8,
// keyframe и last стоят как положено.
func TestSplitVP8RoundTrip(t *testing.T) {
	frame := make([]byte, 3*MaxPartBytes+12345)
	for i := range frame {
		frame[i] = byte(i * 7)
	}
	parts, err := SplitVP8(42, true, 1920, 1080, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 4 {
		t.Fatalf("parts = %d, want >= 4", len(parts))
	}
	var got []byte
	for i, b := range parts {
		if len(b) > MaxPartBytes {
			t.Fatalf("part %d is %d B", i, len(b))
		}
		p, err := ParsePart(b)
		if err != nil {
			t.Fatal(err)
		}
		if !p.VP8 || !p.Keyframe || p.Seq != 42 || p.Width != 1920 || p.Height != 1080 {
			t.Fatalf("part %d header: %+v", i, p)
		}
		if p.Last != (i == len(parts)-1) {
			t.Fatalf("part %d last=%v", i, p.Last)
		}
		for _, tl := range p.Tiles {
			got = append(got, tl.JPEG...)
		}
	}
	if !bytes.Equal(got, frame) {
		t.Fatal("reassembled frame differs")
	}
}

// Сведения о мониторах не затирают флаг VP8.
func TestSetDisplayInfoKeepsVP8(t *testing.T) {
	parts, err := SplitVP8(1, false, 10, 10, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	SetDisplayInfo(parts[0], 2, 1)
	p, err := ParsePart(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !p.VP8 || p.Displays != 2 || p.Display != 1 {
		t.Fatalf("got %+v", p)
	}
}
