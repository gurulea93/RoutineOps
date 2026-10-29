//go:build screen

package screen

import (
	"bytes"
	"testing"
)

func TestProfileHighIsFastAndHasItsOwnBudget(t *testing.T) {
	p, fb := ProfileByName("high")
	if fb || p.Name != "high" || p.FPS != 10 || p.MaxSide != 1920 {
		t.Fatalf("high profile %+v fallback %v", p, fb)
	}
	b := newBandwidthBudget(p, (&fakeClock{}).now)
	if b.rate != 12_000_000/8 {
		t.Fatalf("high must not inherit the low budget: %v B/s", b.rate)
	}
}

// refScale — прежняя посчитанная «в лоб» реализация; новая обязана давать те же байты.
func refScale(dst []byte, w, h int, src []byte, stride, srcW, srcH int) {
	for y := 0; y < h; y++ {
		sy := y * srcH / h
		row := src[sy*stride:]
		d := dst[y*w*4:]
		for x := 0; x < w; x++ {
			s := row[(x*srcW/w)*4:]
			d[x*4+0], d[x*4+1], d[x*4+2], d[x*4+3] = s[2], s[1], s[0], 0xff
		}
	}
}

func TestScaleMatchesReference(t *testing.T) {
	for _, c := range []struct{ srcW, srcH, w, h int }{
		{64, 48, 64, 48},  // без масштаба
		{100, 60, 64, 38}, // уменьшение
		{2560, 1440, 1920, 1080},
	} {
		stride := c.srcW*4 + 16
		src := make([]byte, stride*c.srcH)
		for i := range src {
			src[i] = byte(i * 7)
		}
		got, want := make([]byte, c.w*c.h*4), make([]byte, c.w*c.h*4)
		scaleBGRXToRGBA(got, c.w, c.h, src, stride, c.srcW, c.srcH)
		refScale(want, c.w, c.h, src, stride, c.srcW, c.srcH)
		if !bytes.Equal(got, want) {
			t.Errorf("%dx%d → %dx%d differs from the reference", c.srcW, c.srcH, c.w, c.h)
		}
	}
}
