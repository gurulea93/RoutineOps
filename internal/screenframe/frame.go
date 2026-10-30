//go:build screen

// Package screenframe — канонический формат кадра интерактивного сеанса (RSF1).
//
// Формат ОБЩИЙ для агента, сервера и браузера (web/src/lib/screenframe.ts) и не
// перекодируется нигде по пути захватчик → служба → сервер → браузер. Все числа —
// big-endian (DataView по умолчанию).
//
// Часть кадра:
//
//	[u32 magic 'RSF1'][u32 seq][u16 flags][u16 ntiles][u16 w][u16 h] затем тайлы:
//	[u16 x][u16 y][u16 w][u16 h][u32 len][JPEG]
//
// Поток записей (тело /live и файл записи):
//
//	[u32 len][u32 rel_ms][payload]
//
// Расхождение двух реализаций одного формата проявляется молча — картинка просто не
// появляется, — поэтому обе стороны разбирают ОДИН эталон: testdata/golden.bin.
package screenframe

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	Magic           uint32 = 0x52534631 // 'RSF1'
	HeaderLen              = 16
	TileHeaderLen          = 12
	RecordHeaderLen        = 8

	FlagKeyframe uint16 = 1 << 0
	FlagLast     uint16 = 1 << 1

	// Биты 2–5 — сколько мониторов у машины (0 = агент не сообщает), биты 6–9 — номер
	// того, что сейчас показан. Старые разборщики эти биты игнорируют.
	flagDisplaysShift = 2
	flagDisplayShift  = 6
	flagDisplayMask   = 0xF

	// MaxPartBytes — потолок части: MaxRecvMsgSize gRPC-сервера (4 MiB) общий на весь
	// сервер и не трогается, поэтому кадр режется на части по 256 KiB (proto SessionFrame).
	MaxPartBytes = 256 << 10
)

// ErrFormat — поток не разбирается. Битый поток — конец сеанса, а не «пропустим кадр»:
// тайлы дельтовые, и дальше на холст ляжет каша из двух разных моментов.
var ErrFormat = errors.New("screenframe: malformed")

// Tile — прямоугольник кадра в JPEG.
type Tile struct {
	X, Y, W, H uint16
	JPEG       []byte
}

// Part — часть кадра.
type Part struct {
	Seq      uint32
	Keyframe bool
	Last     bool
	Displays uint8 // мониторов у машины; 0 — агент не сообщил
	Display  uint8 // номер показанного
	Width    uint16
	Height   uint16
	Tiles    []Tile
}

// Size — размер закодированной части.
func (p *Part) Size() int {
	n := HeaderLen
	for _, t := range p.Tiles {
		n += TileHeaderLen + len(t.JPEG)
	}
	return n
}

// AppendPart дописывает закодированную часть к dst.
func AppendPart(dst []byte, p *Part) ([]byte, error) {
	if len(p.Tiles) > 0xffff {
		return dst, fmt.Errorf("%w: %d tiles in one part", ErrFormat, len(p.Tiles))
	}
	var flags uint16
	if p.Keyframe {
		flags |= FlagKeyframe
	}
	if p.Last {
		flags |= FlagLast
	}
	dst = binary.BigEndian.AppendUint32(dst, Magic)
	dst = binary.BigEndian.AppendUint32(dst, p.Seq)
	dst = binary.BigEndian.AppendUint16(dst, flags)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(p.Tiles)))
	dst = binary.BigEndian.AppendUint16(dst, p.Width)
	dst = binary.BigEndian.AppendUint16(dst, p.Height)
	for _, t := range p.Tiles {
		dst = binary.BigEndian.AppendUint16(dst, t.X)
		dst = binary.BigEndian.AppendUint16(dst, t.Y)
		dst = binary.BigEndian.AppendUint16(dst, t.W)
		dst = binary.BigEndian.AppendUint16(dst, t.H)
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(t.JPEG)))
		dst = append(dst, t.JPEG...)
	}
	return dst, nil
}

// ParsePart разбирает одну часть. JPEG тайлов ссылается на b, без копий.
//
// Заявленным длинам не доверяем: они приходят по сети, и аллокация по ним до проверки —
// способ уронить сервер чужим кадром.
func ParsePart(b []byte) (Part, error) {
	if len(b) < HeaderLen {
		return Part{}, fmt.Errorf("%w: frame shorter than header: %d B", ErrFormat, len(b))
	}
	if binary.BigEndian.Uint32(b) != Magic {
		return Part{}, fmt.Errorf("%w: foreign frame magic", ErrFormat)
	}
	flags := binary.BigEndian.Uint16(b[8:])
	n := int(binary.BigEndian.Uint16(b[10:]))
	p := Part{
		Seq:      binary.BigEndian.Uint32(b[4:]),
		Keyframe: flags&FlagKeyframe != 0,
		Last:     flags&FlagLast != 0,
		Displays: uint8(flags >> flagDisplaysShift & flagDisplayMask),
		Display:  uint8(flags >> flagDisplayShift & flagDisplayMask),
		Width:    binary.BigEndian.Uint16(b[12:]),
		Height:   binary.BigEndian.Uint16(b[14:]),
	}
	off := HeaderLen
	if n > 0 {
		p.Tiles = make([]Tile, 0, min(n, (len(b)-off)/TileHeaderLen))
	}
	for i := 0; i < n; i++ {
		if len(b)-off < TileHeaderLen {
			return Part{}, fmt.Errorf("%w: tile %d truncated at header", ErrFormat, i)
		}
		t := Tile{
			X: binary.BigEndian.Uint16(b[off:]),
			Y: binary.BigEndian.Uint16(b[off+2:]),
			W: binary.BigEndian.Uint16(b[off+4:]),
			H: binary.BigEndian.Uint16(b[off+6:]),
		}
		// Сравнение в uint64: на 32-битной платформе int(u32) большой длины отрицателен,
		// прошёл бы проверку и уронил процесс выходом за границы среза.
		declared := binary.BigEndian.Uint32(b[off+8:])
		off += TileHeaderLen
		if uint64(declared) > uint64(len(b)-off) {
			return Part{}, fmt.Errorf("%w: tile %d: declared %d B, %d B left", ErrFormat, i, declared, len(b)-off)
		}
		l := int(declared)
		t.JPEG = b[off : off+l]
		off += l
		p.Tiles = append(p.Tiles, t)
	}
	if off != len(b) {
		return Part{}, fmt.Errorf("%w: %d trailing bytes after tiles", ErrFormat, len(b)-off)
	}
	return p, nil
}

// Split режет кадр на части не больше maxPart байт (жадно, в порядке тайлов). Флаг
// keyframe стоит на КАЖДОЙ части ключевого кадра, last — только на последней. Пустой
// список тайлов даёт одну пустую часть: «кадр без изменений» тоже кадр с номером.
func Split(seq uint32, keyframe bool, width, height uint16, tiles []Tile, maxPart int) ([][]byte, error) {
	if maxPart <= 0 {
		maxPart = MaxPartBytes
	}
	var (
		parts [][]byte
		cur   = Part{Seq: seq, Keyframe: keyframe, Width: width, Height: height}
		size  = HeaderLen
	)
	flush := func(last bool) error {
		cur.Last = last
		b, err := AppendPart(make([]byte, 0, size), &cur)
		if err != nil {
			return err
		}
		parts = append(parts, b)
		cur.Tiles, size = nil, HeaderLen
		return nil
	}
	for _, t := range tiles {
		ts := TileHeaderLen + len(t.JPEG)
		if HeaderLen+ts > maxPart {
			return nil, fmt.Errorf("%w: tile %dx%d at %d,%d is %d B, part limit %d B",
				ErrFormat, t.W, t.H, t.X, t.Y, ts, maxPart)
		}
		if size+ts > maxPart || len(cur.Tiles) == 0xffff {
			if err := flush(false); err != nil {
				return nil, err
			}
		}
		cur.Tiles = append(cur.Tiles, t)
		size += ts
	}
	if err := flush(true); err != nil {
		return nil, err
	}
	return parts, nil
}

// Record — запись потока: часть кадра и её смещение от начала сеанса.
type Record struct {
	RelMs   uint32
	Payload []byte
}

// AppendRecord дописывает запись [u32 len][u32 rel_ms][payload].
func AppendRecord(dst []byte, relMs uint32, payload []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	dst = binary.BigEndian.AppendUint32(dst, relMs)
	return append(dst, payload...)
}

// ParseRecords разбирает поток записей. Payload каждой записи проверяется ParsePart.
func ParseRecords(b []byte) ([]Record, error) {
	var out []Record
	off := 0
	for off < len(b) {
		if len(b)-off < RecordHeaderLen {
			return nil, fmt.Errorf("%w: stream tail shorter than record header", ErrFormat)
		}
		declared := binary.BigEndian.Uint32(b[off:])
		rel := binary.BigEndian.Uint32(b[off+4:])
		off += RecordHeaderLen
		if uint64(declared) > uint64(len(b)-off) {
			return nil, fmt.Errorf("%w: record: declared %d B, %d B left", ErrFormat, declared, len(b)-off)
		}
		l := int(declared)
		payload := b[off : off+l]
		if _, err := ParsePart(payload); err != nil {
			return nil, err
		}
		out = append(out, Record{RelMs: rel, Payload: payload})
		off += l
	}
	return out, nil
}

// SetDisplayInfo вписывает в заголовок готовой части число мониторов и номер текущего
// (оба 0..15). Часть короче заголовка не трогается.
func SetDisplayInfo(part []byte, count, cur int) {
	if len(part) < HeaderLen {
		return
	}
	f := binary.BigEndian.Uint16(part[8:])
	f &^= flagDisplayMask<<flagDisplaysShift | flagDisplayMask<<flagDisplayShift
	f |= uint16(min(max(count, 0), flagDisplayMask))<<flagDisplaysShift |
		uint16(min(max(cur, 0), flagDisplayMask))<<flagDisplayShift
	binary.BigEndian.PutUint16(part[8:], f)
}
