//go:build screen

package screen

import (
	"errors"
	"image"
	"image/color"
	"testing"
)

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestI420FromRGBAKnownColors(t *testing.T) {
	// Опорные значения BT.601 limited range.
	cases := []struct {
		name    string
		c       color.RGBA
		y, u, v byte
	}{
		{"black", color.RGBA{0, 0, 0, 255}, 16, 128, 128},
		{"white", color.RGBA{255, 255, 255, 255}, 235, 128, 128},
		{"red", color.RGBA{255, 0, 0, 255}, 82, 90, 240},
		{"green", color.RGBA{0, 255, 0, 255}, 144, 54, 34},
		{"blue", color.RGBA{0, 0, 255, 255}, 41, 240, 110},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewI420(4, 2)
			if err := f.FromRGBA(solidRGBA(4, 2, tc.c)); err != nil {
				t.Fatal(err)
			}
			for i, got := range f.Y {
				if got != tc.y {
					t.Fatalf("Y[%d]=%d, want %d", i, got, tc.y)
				}
			}
			for i := range f.U {
				if f.U[i] != tc.u || f.V[i] != tc.v {
					t.Fatalf("UV[%d]=(%d,%d), want (%d,%d)", i, f.U[i], f.V[i], tc.u, tc.v)
				}
			}
		})
	}
}

func TestI420OddGeometry(t *testing.T) {
	f := NewI420(5, 3)
	if len(f.Y) != 15 || len(f.U) != 6 || len(f.V) != 6 || f.UVStride != 3 {
		t.Fatalf("planes Y=%d U=%d V=%d uvstride=%d", len(f.Y), len(f.U), len(f.V), f.UVStride)
	}
	// Последний столбец белый, остальное чёрное: краевой блок хромы собран из того, что есть,
	// и обязан остаться нейтральным, а не читать за пределами строки.
	img := solidRGBA(5, 3, color.RGBA{0, 0, 0, 255})
	for y := 0; y < 3; y++ {
		img.SetRGBA(4, y, color.RGBA{255, 255, 255, 255})
	}
	if err := f.FromRGBA(img); err != nil {
		t.Fatal(err)
	}
	if f.Y[4] != 235 || f.Y[14] != 235 || f.Y[0] != 16 {
		t.Fatalf("Y row edges: %v", f.Y)
	}
	for i := range f.U {
		if f.U[i] != 128 || f.V[i] != 128 {
			t.Fatalf("grey input gave chroma (%d,%d) at %d", f.U[i], f.V[i], i)
		}
	}
}

func TestI420FromBGRAMatchesRGBA(t *testing.T) {
	const w, h = 6, 4
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	stride := 4*w + 8 // RowPitch DXGI шире строки
	bgra := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := byte(x*40), byte(y*60), byte((x+y)*20)
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
			p := bgra[y*stride+4*x:]
			p[0], p[1], p[2], p[3] = b, g, r, 255
		}
	}
	a, b := NewI420(w, h), NewI420(w, h)
	if err := a.FromRGBA(img); err != nil {
		t.Fatal(err)
	}
	if err := b.FromBGRA(bgra, stride); err != nil {
		t.Fatal(err)
	}
	for i := range a.Y {
		if a.Y[i] != b.Y[i] {
			t.Fatalf("Y[%d]: rgba %d, bgra %d", i, a.Y[i], b.Y[i])
		}
	}
	for i := range a.U {
		if a.U[i] != b.U[i] || a.V[i] != b.V[i] {
			t.Fatalf("UV[%d] differ", i)
		}
	}
}

func TestI420RejectsMismatch(t *testing.T) {
	f := NewI420(4, 4)
	if err := f.FromRGBA(image.NewRGBA(image.Rect(0, 0, 4, 3))); err == nil {
		t.Fatal("geometry mismatch accepted")
	}
	if err := f.FromBGRA(make([]byte, 4*4*4-1), 16); err == nil {
		t.Fatal("short bgra buffer accepted")
	}
	if err := f.FromBGRA(make([]byte, 64), 12); err == nil {
		t.Fatal("stride < 4*w accepted")
	}
}

func TestParseVP8Frame(t *testing.T) {
	// Ключевой кадр 1365×767: бит 0 = 0, стартовый код, 14-битные размеры (+2 бита масштаба).
	key := []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x55, 0x45, 0xff, 0x02}
	fr, err := ParseVP8Frame(key)
	if err != nil {
		t.Fatal(err)
	}
	if !fr.Key || fr.Width != 1365 || fr.Height != 767 {
		t.Fatalf("got %+v", fr)
	}

	fr, err = ParseVP8Frame([]byte{0x31, 0x02, 0x00})
	if err != nil || fr.Key {
		t.Fatalf("inter frame: %+v, %v", fr, err)
	}

	bad := append([]byte(nil), key...)
	bad[4] = 0x00
	if _, err := ParseVP8Frame(bad); err == nil {
		t.Fatal("missing start code accepted")
	}
	if _, err := ParseVP8Frame(key[:5]); err == nil {
		t.Fatal("truncated keyframe accepted")
	}
	if _, err := ParseVP8Frame(nil); err == nil {
		t.Fatal("empty frame accepted")
	}
}

func TestEncoderConfigDefaults(t *testing.T) {
	c, err := EncoderConfig{Width: 10, Height: 10}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if c.FPS != 15 || c.BitrateKbps != 1000 || c.Threads != 1 {
		t.Fatalf("defaults: %+v", c)
	}
	if _, err := (EncoderConfig{Width: 0, Height: 10}).withDefaults(); err == nil {
		t.Fatal("zero width accepted")
	}
}

func TestUnknownCodecUnavailable(t *testing.T) {
	_, err := NewVideoEncoder("av1", EncoderConfig{Width: 16, Height: 16})
	if !errors.Is(err, ErrCodecUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// Способность объявляется тогда и только тогда, когда кодер создаётся: рассинхрон — ровно
// тот случай, когда сервер выбирает webrtc для агента, который умрёт на первом кадре.
func TestCapabilitiesMatchRegistry(t *testing.T) {
	caps := map[string]bool{}
	for _, c := range VideoCapabilities() {
		caps[c] = true
	}
	for _, codec := range []string{"vp8", "h264"} {
		enc, err := NewVideoEncoder(codec, EncoderConfig{Width: 64, Height: 64})
		built := err == nil
		if enc != nil {
			enc.Close()
		}
		if built != caps["screen_webrtc_"+codec] {
			t.Fatalf("%s: encoder built=%v, capability=%v (err %v)", codec, built, caps["screen_webrtc_"+codec], err)
		}
	}
}
