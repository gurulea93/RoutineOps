//go:build screen

package screenframe

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func goldenPart() *Part {
	return &Part{
		Seq: 42, Keyframe: true, Last: true, Width: 192, Height: 160,
		Tiles: []Tile{
			{X: 0, Y: 0, W: 128, H: 128, JPEG: []byte{0xff, 0xd8, 0xff, 0x01}},
			{X: 128, Y: 0, W: 64, H: 128, JPEG: []byte{0xff, 0xd8, 0xff, 0x02, 0x03}},
			{X: 0, Y: 128, W: 128, H: 32, JPEG: []byte{0xff, 0xd8, 0xff}},
		},
	}
}

// Эталон собирает Go, разбирает и он, и web/src/lib/screenframe.test.ts. Если этот тест
// падает — изменился формат, и TypeScript-сторона обязана поменяться вместе с ним.
func TestGoldenMatchesCheckedInFixture(t *testing.T) {
	want, err := os.ReadFile("testdata/golden.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := AppendPart(nil, goldenPart())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded golden differs from testdata/golden.bin:\n got %x\nwant %x", got, want)
	}
	p, err := ParsePart(want)
	if err != nil {
		t.Fatal(err)
	}
	if p.Seq != 42 || !p.Keyframe || !p.Last || p.Width != 192 || p.Height != 160 || len(p.Tiles) != 3 {
		t.Fatalf("parsed %+v", p)
	}
	if p.Tiles[1].X != 128 || p.Tiles[1].W != 64 || !bytes.Equal(p.Tiles[1].JPEG, []byte{0xff, 0xd8, 0xff, 0x02, 0x03}) {
		t.Fatalf("tile 1 %+v", p.Tiles[1])
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	g, _ := os.ReadFile("testdata/golden.bin")
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), g...)) }
	cases := map[string][]byte{
		"magic":       mut(func(b []byte) []byte { b[0] = 0xde; return b }),
		"short":       g[:20],
		"tile length": mut(func(b []byte) []byte { b[16+8], b[16+9] = 0xff, 0xff; return b }),
		"trailing":    append(append([]byte(nil), g...), 0),
		"empty":       nil,
		// Длина ≥ 2^31: на 32-битной платформе int(u32) отрицателен (тот самый баг).
		"huge length": mut(func(b []byte) []byte { b[16+8] = 0x80; return b }),
	}
	for name, b := range cases {
		if _, err := ParsePart(b); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSplitRespectsLimitAndFlags(t *testing.T) {
	var tiles []Tile
	for i := 0; i < 10; i++ {
		tiles = append(tiles, Tile{X: uint16(i * 64), W: 64, H: 64, JPEG: bytes.Repeat([]byte{byte(i)}, 1000)})
	}
	const limit = 2600 // 2 тайла на часть
	parts, err := Split(7, true, 640, 64, tiles, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 5 {
		t.Fatalf("parts = %d", len(parts))
	}
	n := 0
	for i, b := range parts {
		if len(b) > limit {
			t.Fatalf("part %d is %d B", i, len(b))
		}
		p, err := ParsePart(b)
		if err != nil {
			t.Fatal(err)
		}
		if p.Seq != 7 || !p.Keyframe || p.Last != (i == len(parts)-1) {
			t.Fatalf("part %d flags %+v", i, p)
		}
		for _, tl := range p.Tiles {
			if tl.X != uint16(n*64) {
				t.Fatalf("tile order broken at %d", n)
			}
			n++
		}
	}
	if n != 10 {
		t.Fatalf("tiles = %d", n)
	}

	if _, err := Split(1, false, 64, 64, []Tile{{W: 64, H: 64, JPEG: make([]byte, 5000)}}, 1000); !errors.Is(err, ErrFormat) {
		t.Fatalf("oversized tile: %v", err)
	}
	empty, err := Split(2, false, 64, 64, nil, 0)
	if err != nil || len(empty) != 1 {
		t.Fatalf("empty frame: %d parts, %v", len(empty), err)
	}
	if p, _ := ParsePart(empty[0]); !p.Last || len(p.Tiles) != 0 {
		t.Fatalf("empty part %+v", p)
	}
}

func TestRecords(t *testing.T) {
	g, _ := os.ReadFile("testdata/golden.bin")
	var stream []byte
	stream = AppendRecord(stream, 0, g)
	stream = AppendRecord(stream, 1500, g)
	recs, err := ParseRecords(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].RelMs != 1500 || !bytes.Equal(recs[0].Payload, g) {
		t.Fatalf("records %+v", recs)
	}
	if _, err := ParseRecords(stream[:len(stream)-1]); !errors.Is(err, ErrFormat) {
		t.Fatalf("truncated stream: %v", err)
	}
}

func TestRecordHugeLength(t *testing.T) {
	stream := []byte{0xff, 0xff, 0xff, 0xf0, 0, 0, 0, 0, 1, 2, 3}
	if _, err := ParseRecords(stream); !errors.Is(err, ErrFormat) {
		t.Fatalf("err = %v", err)
	}
}
