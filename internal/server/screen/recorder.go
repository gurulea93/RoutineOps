//go:build screen

package screen

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/at-wat/ebml-go/webm"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

// Recorder пишет VP8-поток сеанса в .webm (Этап B, §5). Запись делает сервер — сторона, у
// которой нет интереса её подделать; поэтому в P2P записи нет и P2P там, где она требуется,
// запрещён (policy.go).
//
// .webm, а не собственный контейнер: файл воспроизводится прямо в <video> панели без
// перекодирования. Запись начинается с первого КЛЮЧЕВОГО кадра — до него VP8 декодировать
// нечем, а геометрия дорожки (PixelWidth/Height) известна только из его заголовка.
//
// Квота тенанта: кадр, который её перешёл бы, не пишется, файл закрывается корректно (его
// можно воспроизвести), Truncated() == true. Молча остановившаяся запись выглядела бы как
// полная.
type Recorder struct {
	mu        sync.Mutex
	out       *countingWriter
	quota     int64
	sb        *samplebuilder.SampleBuilder
	block     webm.BlockWriteCloser
	baseTS    uint32
	planned   int64 // оценка размера файла сверху: см. framingOverhead
	truncated bool
	closed    bool
	frames    int
}

// Квота проверяется по ОЦЕНКЕ сверху, а не по записанным байтам: ebml-go пишет из своей
// горутины, и счётчик выхода отстаёт от последнего Write — по нему квота перескакивалась бы.
// framingOverhead — запас на заголовки SimpleBlock/Cluster на кадр, headerOverhead — EBML,
// Segment, Info, Tracks и финальный Cluster при закрытии.
const (
	framingOverhead = 64
	headerOverhead  = 1024
)

// NewRecorder пишет в w; quotaBytes <= 0 — без квоты. w закрывается в Close.
func NewRecorder(w io.WriteCloser, quotaBytes int64) *Recorder {
	return &Recorder{
		out:   &countingWriter{w: w},
		quota: quotaBytes,
		sb:    samplebuilder.New(128, &codecs.VP8Packet{}, 90000),
	}
}

// WriteRTP принимает RTP-пакет VP8 из входящей дорожки агента.
func (r *Recorder) WriteRTP(p *rtp.Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.truncated {
		return nil
	}
	r.sb.Push(p)
	for s := r.sb.Pop(); s != nil; s = r.sb.Pop() {
		if err := r.writeFrame(s.Data, s.PacketTimestamp); err != nil {
			return err
		}
		if r.truncated {
			return nil
		}
	}
	return nil
}

func (r *Recorder) writeFrame(frame []byte, ts uint32) error {
	key, w, h, ok := vp8KeyframeInfo(frame)
	if r.block == nil {
		if !ok || !key {
			return nil // ждём ключевой кадр
		}
		ws, err := webm.NewSimpleBlockWriter(r.out, []webm.TrackEntry{{
			Name: "Screen", TrackNumber: 1, TrackUID: 1, CodecID: "V_VP8", TrackType: 1,
			Video: &webm.Video{PixelWidth: uint64(w), PixelHeight: uint64(h)},
		}})
		if err != nil {
			return fmt.Errorf("screen: webm writer: %w", err)
		}
		r.block, r.baseTS, r.planned = ws[0], ts, headerOverhead
	}
	next := r.planned + int64(len(frame)) + framingOverhead
	if r.quota > 0 && next > r.quota {
		r.truncated = true
		return r.closeLocked()
	}
	ms := int64(ts-r.baseTS) / 90 // RTP 90 кГц → мс; вычитание uint32 переживает переполнение
	if _, err := r.block.Write(key, ms, frame); err != nil {
		return fmt.Errorf("screen: webm write: %w", err)
	}
	r.planned = next
	r.frames++
	return nil
}

// Close завершает файл. Повторный вызов безопасен.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeLocked()
}

func (r *Recorder) closeLocked() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.block != nil {
		return r.block.Close() // закрывает и r.out
	}
	return r.out.Close()
}

// Truncated — запись оборвана квотой.
func (r *Recorder) Truncated() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.truncated }

// Bytes — сколько записано в файл (точно — после Close).
func (r *Recorder) Bytes() int64 { return r.out.n.Load() }

// Frames — сколько кадров записано.
func (r *Recorder) Frames() int { r.mu.Lock(); defer r.mu.Unlock(); return r.frames }

type countingWriter struct {
	w io.WriteCloser
	n atomic.Int64 // пишет горутина ebml-go, читает Bytes()
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingWriter) Close() error { return c.w.Close() }

// vp8KeyframeInfo читает заголовок кадра VP8 (RFC 6386 §9.1). ok=false — кадр битый.
// Копия парсера агента (internal/agent/screen.ParseVP8Frame): серверу нельзя импортировать
// агентский пакет.
func vp8KeyframeInfo(b []byte) (key bool, w, h int, ok bool) {
	if len(b) < 3 {
		return false, 0, 0, false
	}
	if b[0]&1 != 0 {
		return false, 0, 0, true
	}
	if len(b) < 10 || b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a {
		return false, 0, 0, false
	}
	w = int(uint16(b[6])|uint16(b[7])<<8) & 0x3fff
	h = int(uint16(b[8])|uint16(b[9])<<8) & 0x3fff
	return true, w, h, true
}
