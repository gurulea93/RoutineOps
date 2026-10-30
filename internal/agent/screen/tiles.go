//go:build screen

package screen

import (
	"bytes"
	"image"
	"image/jpeg"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Кодер кадров RSF1 (Этап A): кадр делится на тайлы, в поток идут только изменившиеся,
// каждый — отдельным JPEG. Экран рабочего стола в покое почти не меняется, и «кадр без
// изменений» стоит один заголовок части.

// Profile — потолки профиля (миграция 075): частота и длинная сторона кадра, плюс бюджет
// полосы петли отправки.
type Profile struct {
	Name    string
	FPS     int
	MaxSide int
	Quality int

	// MaxBitsPerSec и MaxPause — бюджет полосы и потолок паузы перед отправкой кадра
	// (упор в потолок считается в SessionStats.stalls). 🔴 Решение проекта от 29.10.2026,
	// а НЕ замер: 1.5 Мбит/с согласуется с оценкой из миграции 075 («23.8 кбит/с —
	// 1.6–1.7 % бюджета low»), но сам бюджет нигде не измерялся. Нули — бюджет не задан.
	MaxBitsPerSec int
	MaxPause      time.Duration
}

var (
	ProfileLow = Profile{Name: "low", FPS: 1, MaxSide: 1280, Quality: 60,
		MaxBitsPerSec: 1_500_000, MaxPause: time.Second}
	// Бюджет medium не задан: в Ф1 решён только low (29.10.2026).
	ProfileMedium = Profile{Name: "medium", FPS: 2, MaxSide: 1920, Quality: 70}
	// high — для управления мышью: 10 к/с. Бюджет 12 Мбит/с рассчитан на LAN и офисный канал
	// (изменившиеся тайлы: ~20 КБ на кадр при перетаскивании окон, то есть ~1.6 Мбит/с);
	// пауза до 0.5 с, чтобы картинка не отставала от курсора. Решение проекта, а не замер.
	ProfileHigh = Profile{Name: "high", FPS: 10, MaxSide: 1920, Quality: 70,
		MaxBitsPerSec: 12_000_000, MaxPause: 500 * time.Millisecond}
	// ultra — «как вживую»: 30 к/с, родное разрешение (до 4K), JPEG quality 88. Рассчитан на
	// LAN и хороший офисный канал; бюджет 80 Мбит/с — потолок, а не ожидаемая нагрузка:
	// в покое уходят только заголовки, а изменившиеся тайлы — десятки КБ.
	ProfileUltra = Profile{Name: "ultra", FPS: 30, MaxSide: 3840, Quality: 88,
		MaxBitsPerSec: 80_000_000, MaxPause: 250 * time.Millisecond}
)

// ProfileByName — неизвестное имя даёт low и fallback=true: агент не роняет сеанс из-за
// рассинхрона версий, но и не скрывает его (SessionHello.profile_fallback).
func ProfileByName(name string) (p Profile, fallback bool) {
	switch name {
	case "low", "":
		return ProfileLow, name != "low" && name != ""
	case "medium":
		return ProfileMedium, false
	case "high":
		return ProfileHigh, false
	case "ultra":
		return ProfileUltra, false
	default:
		return ProfileLow, true
	}
}

// TileSize — сторона тайла. 128 — компромисс: мельче — больше заголовков и хуже сжатие
// JPEG, крупнее — мигающий курсор тянет за собой лишние килобайты.
const TileSize = 128

// FitSide — геометрия кадра после ограничения длинной стороны (пропорции сохраняются).
func FitSide(w, h, maxSide int) (int, int) {
	if maxSide <= 0 || (w <= maxSide && h <= maxSide) {
		return w, h
	}
	if w >= h {
		return maxSide, max(1, h*maxSide/w)
	}
	return max(1, w*maxSide/h), maxSide
}

// TileEncoder помнит предыдущий кадр и выдаёт изменившиеся тайлы.
type TileEncoder struct {
	srcW, srcH int
	W, H       int
	quality    int
	cur, prev  []byte // RGBA кадра в целевой геометрии
	have       bool
}

func NewTileEncoder(srcW, srcH int, p Profile) *TileEncoder {
	w, h := FitSide(srcW, srcH, p.MaxSide)
	return &TileEncoder{srcW: srcW, srcH: srcH, W: w, H: h, quality: p.Quality,
		cur: make([]byte, w*h*4), prev: make([]byte, w*h*4)}
}

// Encode превращает кадр BGRX в список тайлов. keyframe — все тайлы, иначе только
// изменившиеся с прошлого Encode. Первый вызов всегда полный.
func (e *TileEncoder) Encode(pix []byte, stride int, keyframe bool) ([]screenframe.Tile, error) {
	scaleBGRXToRGBA(e.cur, e.W, e.H, pix, stride, e.srcW, e.srcH)
	full := keyframe || !e.have
	var tiles []screenframe.Tile
	var buf bytes.Buffer
	for ty := 0; ty < e.H; ty += TileSize {
		for tx := 0; tx < e.W; tx += TileSize {
			tw, th := min(TileSize, e.W-tx), min(TileSize, e.H-ty)
			if !full && !e.changed(tx, ty, tw, th) {
				continue
			}
			img := &image.RGBA{
				Pix:    e.cur[(ty*e.W+tx)*4:],
				Stride: e.W * 4,
				Rect:   image.Rect(0, 0, tw, th),
			}
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: e.quality}); err != nil {
				return nil, err
			}
			tiles = append(tiles, screenframe.Tile{
				X: uint16(tx), Y: uint16(ty), W: uint16(tw), H: uint16(th),
				JPEG: bytes.Clone(buf.Bytes()),
			})
		}
	}
	e.cur, e.prev = e.prev, e.cur
	e.have = true
	return tiles, nil
}

// changed сравнивает прямоугольник текущего кадра с предыдущим (оба в e.cur/e.prev до
// обмена буферов).
func (e *TileEncoder) changed(x, y, w, h int) bool {
	for row := y; row < y+h; row++ {
		off := (row*e.W + x) * 4
		if !bytes.Equal(e.cur[off:off+w*4], e.prev[off:off+w*4]) {
			return true
		}
	}
	return false
}

// scaleBGRXToRGBA — ближайший сосед из BGRX (srcW×srcH, stride) в RGBA dst (w×h). Для
// текста на экране ближайший сосед честнее билинейного: буквы остаются резкими, а не
// размытыми, а 1280 по длинной стороне выбраны так, чтобы шрифт оставался читаемым.
func scaleBGRXToRGBA(dst []byte, w, h int, src []byte, stride, srcW, srcH int) {
	if w == srcW && h == srcH {
		// Без масштаба: только перестановка каналов, без делений на каждый пиксель.
		for y := 0; y < h; y++ {
			row := src[y*stride : y*stride+w*4]
			d := dst[y*w*4 : (y+1)*w*4]
			for x := 0; x < len(row); x += 4 {
				d[x+0], d[x+1], d[x+2], d[x+3] = row[x+2], row[x+1], row[x], 0xff
			}
		}
		return
	}
	xs := make([]int, w) // исходный столбец для каждого целевого: делим один раз на кадр, а не на пиксель
	for x := range xs {
		xs[x] = (x * srcW / w) * 4
	}
	for y := 0; y < h; y++ {
		sy := y * srcH / h
		row := src[sy*stride:]
		d := dst[y*w*4:]
		for x, sx := range xs {
			d[x*4+0] = row[sx+2]
			d[x*4+1] = row[sx+1]
			d[x*4+2] = row[sx]
			d[x*4+3] = 0xff
		}
	}
}
