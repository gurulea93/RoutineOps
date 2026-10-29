//go:build screen && linux

package screen

import (
	"os"
	"testing"
)

// Нужен живой X-сервер: в CI/workspace — `Xvfb :99 -screen 0 1280x800x24 & DISPLAY=:99 go test ...`.
func TestX11CaptureLive(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set (run under Xvfb)")
	}
	c, err := NewScreenCapturer()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w, h := c.Size()
	if w <= 0 || h <= 0 {
		t.Fatalf("size %dx%d", w, h)
	}
	for i := 0; i < 3; i++ {
		pix, stride, err := c.Capture()
		if err != nil {
			t.Fatal(err)
		}
		if stride != 4*w || len(pix) < stride*h {
			t.Fatalf("frame %d: stride %d len %d for %dx%d", i, stride, len(pix), w, h)
		}
	}
	// Кадр проходит конвертер в геометрии профиля.
	fw, fh := FitGeometry(w, h, ProfileLowMaxWidth, ProfileLowMaxHeight)
	pix, stride, _ := c.Capture()
	if err := NewI420(fw, fh).FromBGRAScaled(pix, stride, w, h); err != nil {
		t.Fatal(err)
	}
}
