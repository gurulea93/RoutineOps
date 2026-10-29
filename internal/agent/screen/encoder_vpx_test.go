//go:build screen && vpx && cgo

package screen

import (
	"image"
	"image/color"
	"testing"
)

// syntheticFrame — «рабочий стол»: градиент-обои и окно, которое двигается на step пикселей.
func syntheticFrame(w, h, step int) *I420 {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{byte(x * 255 / w), byte(y * 255 / h), 160, 255})
		}
	}
	bx, by := (step*7)%(w-40), (step*3)%(h-30)
	for y := by; y < by+30; y++ {
		for x := bx; x < bx+40; x++ {
			img.SetRGBA(x, y, color.RGBA{240, 240, 240, 255})
		}
	}
	f := NewI420(w, h)
	if err := f.FromRGBA(img); err != nil {
		panic(err)
	}
	return f
}

func newTestVP8(t *testing.T, w, h int) Encoder {
	t.Helper()
	enc, err := NewVideoEncoder("vp8", EncoderConfig{Width: w, Height: h, FPS: 15, BitrateKbps: 500})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(enc.Close)
	return enc
}

func TestVP8EncodesSyntheticFrames(t *testing.T) {
	for _, g := range []struct{ w, h int }{{320, 240}, {321, 241}} {
		enc := newTestVP8(t, g.w, g.h)

		out, err := enc.Encode(syntheticFrame(g.w, g.h, 0), false)
		if err != nil {
			t.Fatal(err)
		}
		fr, err := ParseVP8Frame(out)
		if err != nil {
			t.Fatalf("%dx%d first frame: %v", g.w, g.h, err)
		}
		if !fr.Key || fr.Width != g.w || fr.Height != g.h {
			t.Fatalf("%dx%d first frame: %+v", g.w, g.h, fr)
		}

		inter := 0
		for i := 1; i < 30; i++ {
			out, err := enc.Encode(syntheticFrame(g.w, g.h, i), false)
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			if out == nil {
				continue // пропуск rate control — норма
			}
			fr, err := ParseVP8Frame(out)
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			if !fr.Key {
				inter++
			}
		}
		if inter < 20 {
			t.Fatalf("%dx%d: only %d inter frames of 29 — kf_mode/lag misconfigured", g.w, g.h, inter)
		}
	}
}

func TestVP8ForceKeyframe(t *testing.T) {
	enc := newTestVP8(t, 320, 240)
	for i := 0; i < 5; i++ {
		if _, err := enc.Encode(syntheticFrame(320, 240, i), false); err != nil {
			t.Fatal(err)
		}
	}
	out, err := enc.Encode(syntheticFrame(320, 240, 5), true)
	if err != nil {
		t.Fatal(err)
	}
	fr, err := ParseVP8Frame(out)
	if err != nil || !fr.Key {
		t.Fatalf("forced frame: %+v, %v", fr, err)
	}
}

// Статичный экран — основной случай удалённого стола: после ключевого кадра почти ничего.
func TestVP8StaticScreenIsCheap(t *testing.T) {
	enc := newTestVP8(t, 320, 240)
	f := syntheticFrame(320, 240, 0)
	key, err := enc.Encode(f, false)
	if err != nil {
		t.Fatal(err)
	}
	var last []byte
	for i := 0; i < 10; i++ {
		if last, err = enc.Encode(f, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(last)*10 > len(key) {
		t.Fatalf("static frame %d bytes vs keyframe %d", len(last), len(key))
	}
}

func TestVP8SetBitrate(t *testing.T) {
	enc := newTestVP8(t, 320, 240)
	if err := enc.SetBitrate(200); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Encode(syntheticFrame(320, 240, 0), false); err != nil {
		t.Fatal(err)
	}
	if err := enc.SetBitrate(0); err == nil {
		t.Fatal("zero bitrate accepted")
	}
}

func TestVP8RejectsWrongGeometryAndClosed(t *testing.T) {
	enc, err := NewVideoEncoder("vp8", EncoderConfig{Width: 320, Height: 240})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Encode(NewI420(160, 120), false); err == nil {
		t.Fatal("wrong geometry accepted")
	}
	short := NewI420(320, 240)
	short.Y = short.Y[:100]
	if _, err := enc.Encode(short, false); err == nil {
		t.Fatal("short plane accepted")
	}
	enc.Close()
	enc.Close() // повторный Close безопасен
	if _, err := enc.Encode(NewI420(320, 240), false); err == nil {
		t.Fatal("encode after Close accepted")
	}
}

func TestVP8CapabilityAdvertised(t *testing.T) {
	caps := VideoCapabilities()
	if len(caps) == 0 || caps[0] != "screen_webrtc_vp8" {
		t.Fatalf("capabilities: %v", caps)
	}
}
