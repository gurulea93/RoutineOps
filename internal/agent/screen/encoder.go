//go:build screen

package screen

import (
	"errors"
	"fmt"
	"image"
	"sort"
	"sync"
)

// Видеокодер Этапа B (docs/REMOTE_DESKTOP_B_DESIGN.md §6).
//
// Интерфейс заменяемый: VP8 через libvpx сегодня, аппаратный H.264 потом. Реализации
// регистрируются сами (init под своим build-тегом), поэтому сборка без libvpx не содержит
// ни строчки CGO и честно отвечает ErrCodecUnavailable — агент тогда остаётся на RSF1 и НЕ
// объявляет способность screen_webrtc_<codec>. Объявить способность, которой нет, значило бы
// получить от сервера transport="webrtc" и умереть на первом кадре.

// ErrCodecUnavailable — кодек не собран в этот бинарь (нет тега vpx, нет CGO).
var ErrCodecUnavailable = errors.New("screen: video codec unavailable in this build")

// I420 — планарный YUV 4:2:0, вход любого кодера. Хрома — (w+1)/2 × (h+1)/2: нечётная
// геометрия (окно RDP 1365×767) допустима и не обрезается.
type I420 struct {
	Width, Height int
	Y, U, V       []byte
	YStride       int
	UVStride      int
}

// NewI420 выделяет кадр под геометрию w×h.
func NewI420(w, h int) *I420 {
	cw, ch := (w+1)/2, (h+1)/2
	return &I420{
		Width: w, Height: h,
		Y:       make([]byte, w*h),
		U:       make([]byte, cw*ch),
		V:       make([]byte, cw*ch),
		YStride: w, UVStride: cw,
	}
}

// FromRGBA заполняет кадр из image.RGBA той же геометрии (захват X11/macOS).
func (f *I420) FromRGBA(img *image.RGBA) error {
	b := img.Bounds()
	if b.Dx() != f.Width || b.Dy() != f.Height {
		return fmt.Errorf("screen: i420 %dx%d, source %dx%d", f.Width, f.Height, b.Dx(), b.Dy())
	}
	off := img.PixOffset(b.Min.X, b.Min.Y)
	f.fromPacked(img.Pix[off:], img.Stride, f.Width, f.Height, 0, 2)
	return nil
}

// FromBGRA заполняет кадр из упакованного BGRA (DXGI отдаёт ровно его; stride — RowPitch,
// он бывает больше 4*w).
func (f *I420) FromBGRA(pix []byte, stride int) error {
	return f.FromBGRAScaled(pix, stride, f.Width, f.Height)
}

// FromBGRAScaled — то же с даунскейлом исходника srcW×srcH до геометрии кадра (потолок
// профиля). Ближайший сосед: для экрана с текстом он честнее размытия, а стоит одно деление
// на пиксель. Апскейл не нужен и не поддерживается.
func (f *I420) FromBGRAScaled(pix []byte, stride, srcW, srcH int) error {
	if srcW < f.Width || srcH < f.Height {
		return fmt.Errorf("screen: upscale %dx%d → %dx%d not supported", srcW, srcH, f.Width, f.Height)
	}
	if stride < 4*srcW || len(pix) < stride*(srcH-1)+4*srcW {
		return fmt.Errorf("screen: bgra buffer too small for %dx%d (stride %d, len %d)", srcW, srcH, stride, len(pix))
	}
	f.fromPacked(pix, stride, srcW, srcH, 2, 0)
	return nil
}

// fromPacked — BT.601 limited range (16..235), как ждут VP8 и браузерный декодер по
// умолчанию. ri/bi — смещения R и B в 4-байтовом пикселе, G всегда в 1. Хрома — среднее
// блока 2×2 кадра (на краю нечётной геометрии — того, что есть). Пиксель кадра (x, y) берётся
// из (x*srcW/w, y*srcH/h) исходника; при равной геометрии это тождество.
func (f *I420) fromPacked(pix []byte, stride, srcW, srcH, ri, bi int) {
	w, h := f.Width, f.Height
	for y := 0; y < h; y++ {
		row := pix[(y*srcH/h)*stride:]
		dst := f.Y[y*f.YStride:]
		for x := 0; x < w; x++ {
			p := row[4*(x*srcW/w):]
			r, g, b := int(p[ri]), int(p[1]), int(p[bi])
			dst[x] = byte((66*r+129*g+25*b+128)>>8 + 16)
		}
	}
	for cy := 0; cy < (h+1)/2; cy++ {
		for cx := 0; cx < (w+1)/2; cx++ {
			var rs, gs, bs, n int
			for dy := 0; dy < 2; dy++ {
				y := 2*cy + dy
				if y >= h {
					break
				}
				for dx := 0; dx < 2; dx++ {
					x := 2*cx + dx
					if x >= w {
						break
					}
					p := pix[(y*srcH/h)*stride+4*(x*srcW/w):]
					rs += int(p[ri])
					gs += int(p[1])
					bs += int(p[bi])
					n++
				}
			}
			r, g, b := (rs+n/2)/n, (gs+n/2)/n, (bs+n/2)/n
			f.U[cy*f.UVStride+cx] = byte((-38*r-74*g+112*b+128)>>8 + 128)
			f.V[cy*f.UVStride+cx] = byte((112*r-94*g-18*b+128)>>8 + 128)
		}
	}
}

// EncoderConfig — параметры кодера. Геометрия фиксирована на весь сеанс: смена разрешения
// (поворот экрана, смена монитора) — новый кодер и ключевой кадр, а не reconfigure на лету.
type EncoderConfig struct {
	Width, Height int
	FPS           int // 0 → 15, потолок профиля low
	BitrateKbps   int // 0 → 1000
	Threads       int // 0 → 1: агент делит машину с сотрудником
}

func (c EncoderConfig) withDefaults() (EncoderConfig, error) {
	if c.Width <= 0 || c.Height <= 0 {
		return c, fmt.Errorf("screen: bad encoder geometry %dx%d", c.Width, c.Height)
	}
	if c.FPS <= 0 {
		c.FPS = 15
	}
	if c.BitrateKbps <= 0 {
		c.BitrateKbps = 1000
	}
	if c.Threads <= 0 {
		c.Threads = 1
	}
	return c, nil
}

// Encoder кодирует I420 в кадры кодека. НЕ потокобезопасен: один кодер — одна петля кадров.
type Encoder interface {
	// Encode возвращает закодированный кадр. nil без ошибки — rate control пропустил кадр
	// (норма при упоре в битрейт). forceKey — ответ на keyframe_request (новый зритель, PLI).
	Encode(frame *I420, forceKey bool) ([]byte, error)
	// SetBitrate — адаптация по RTCP (REMB/потери) без пересоздания кодера.
	SetBitrate(kbps int) error
	Close()
}

type encoderFactory func(EncoderConfig) (Encoder, error)

var (
	encodersMu sync.RWMutex
	encoders   = map[string]encoderFactory{}
)

func registerEncoder(codec string, f encoderFactory) {
	encodersMu.Lock()
	defer encodersMu.Unlock()
	encoders[codec] = f
}

// NewVideoEncoder создаёт кодер по имени из ScreenSessionCommand.video_codec.
func NewVideoEncoder(codec string, cfg EncoderConfig) (Encoder, error) {
	encodersMu.RLock()
	f, ok := encoders[codec]
	encodersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrCodecUnavailable, codec)
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return f(cfg)
}

// VideoCapabilities — способности для devices.capabilities: screen_webrtc_<codec> на каждый
// собранный кодек. Пусто — агент умеет только RSF1.
func VideoCapabilities() []string {
	encodersMu.RLock()
	defer encodersMu.RUnlock()
	caps := make([]string, 0, len(encoders))
	for codec := range encoders {
		caps = append(caps, "screen_webrtc_"+codec)
	}
	sort.Strings(caps)
	return caps
}

// VP8Frame — то, что видно из заголовка кадра VP8 (RFC 6386 §9.1) без декодера.
type VP8Frame struct {
	Key           bool
	Width, Height int // только для ключевого кадра
}

// ParseVP8Frame разбирает заголовок кадра VP8. Нужен отправителю RTP (ключевой кадр —
// граница, с которой можно начинать новому зрителю) и тестам кодера.
func ParseVP8Frame(b []byte) (VP8Frame, error) {
	if len(b) < 3 {
		return VP8Frame{}, errors.New("screen: vp8 frame too short")
	}
	var fr VP8Frame
	fr.Key = b[0]&1 == 0
	if !fr.Key {
		return fr, nil
	}
	if len(b) < 10 {
		return VP8Frame{}, errors.New("screen: vp8 keyframe header too short")
	}
	if b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a {
		return VP8Frame{}, errors.New("screen: vp8 keyframe start code missing")
	}
	fr.Width = int(uint16(b[6])|uint16(b[7])<<8) & 0x3fff
	fr.Height = int(uint16(b[8])|uint16(b[9])<<8) & 0x3fff
	return fr, nil
}
