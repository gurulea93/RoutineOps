//go:build screen

package screen

import (
	"fmt"
	"time"
)

// Видео VP8 поверх потока RSF1 — схема RustDesk: кадры кодека идут тем же путём, что и
// JPEG-тайлы (gRPC до сервера, HTTPS до браузера), а не по UDP. Браузер декодирует их сам
// (WebCodecs). Поэтому видео работает везде, где открывается панель: за VPN, в браузере с
// запретом UDP без прокси (Opera), через корпоративный прокси.

// vp8KeyframeEvery — периодический ключевой кадр в этом режиме. Чаще, чем у JPEG (30 с):
// новый зритель и отставший после сброса буфера начинают с ключевого кадра, и цепочка дельт
// до него должна быть короткой.
const vp8KeyframeEvery = 5 * time.Second

// vp8Frames — кодер кадров захвата в VP8 под профиль.
type vp8Frames struct {
	enc   Encoder
	frame *I420
	w, h  int
	fps   int
}

// vp8Params — частота и битрейт VP8 для профиля. Битрейт не выше 75 % бюджета полосы
// профиля: петля отправки держит этот бюджет, и поток выше него она бы душила паузами.
func vp8Params(profile string) (fps, kbps, maxW, maxH int) {
	p := webrtcParams(profile)
	prof, _ := ProfileByName(profile)
	budget := prof.MaxBitsPerSec
	if budget <= 0 {
		budget = ProfileLow.MaxBitsPerSec
	}
	return p.fps, min(p.kbps, budget*3/4/1000), p.w, p.h
}

// newVP8Frames — nil и ошибка, если кодека в сборке нет (агент без libvpx): тогда петля
// остаётся на JPEG-тайлах.
func newVP8Frames(srcW, srcH int, profile string) (*vp8Frames, error) {
	fps, kbps, maxW, maxH := vp8Params(profile)
	w, h := FitGeometry(srcW, srcH, maxW, maxH)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("screen: geometry %dx%d", srcW, srcH)
	}
	threads := 1 // агент делит машину с сотрудником
	if w*h > 1920*1080 {
		threads = 2 // 4K за один поток не успевает в 30 к/с
	}
	enc, err := NewVideoEncoder("vp8", EncoderConfig{Width: w, Height: h, FPS: fps, BitrateKbps: kbps, Threads: threads})
	if err != nil {
		return nil, err
	}
	return &vp8Frames{enc: enc, frame: NewI420(w, h), w: w, h: h, fps: fps}, nil
}

// encode кодирует захваченный кадр. nil без ошибки — кодер пропустил кадр (rate control).
// key — фактически ключевой ли вышел кадр (кодер ставит их и сам).
func (v *vp8Frames) encode(pix []byte, stride, srcW, srcH int, forceKey bool) (out []byte, key bool, err error) {
	if err := v.frame.FromBGRAScaled(pix, stride, srcW, srcH); err != nil {
		return nil, false, err
	}
	out, err = v.enc.Encode(v.frame, forceKey)
	if err != nil || out == nil {
		return nil, false, err
	}
	f, err := ParseVP8Frame(out)
	if err != nil {
		return nil, false, err
	}
	return out, f.Key, nil
}

func (v *vp8Frames) close() { v.enc.Close() }
