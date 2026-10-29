//go:build screen

package screen

import (
	"bytes"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/webm"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

type parsedWebM struct {
	codec  string
	w, h   uint64
	blocks []ebml.Block
}

func parseWebM(t *testing.T, b []byte) parsedWebM {
	t.Helper()
	var doc struct {
		Header  webm.EBMLHeader `ebml:"EBML"`
		Segment webm.Segment    `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(b), &doc); err != nil {
		t.Fatalf("recording is not valid webm: %v", err)
	}
	if doc.Header.DocType != "webm" || len(doc.Segment.Tracks.TrackEntry) != 1 {
		t.Fatalf("doctype %q, tracks %d", doc.Header.DocType, len(doc.Segment.Tracks.TrackEntry))
	}
	tr := doc.Segment.Tracks.TrackEntry[0]
	p := parsedWebM{codec: tr.CodecID}
	if tr.Video != nil {
		p.w, p.h = tr.Video.PixelWidth, tr.Video.PixelHeight
	}
	for _, c := range doc.Segment.Cluster {
		p.blocks = append(p.blocks, c.SimpleBlock...)
	}
	return p
}

// rtpFrames нарезает кадры в RTP так же, как это делает отправитель pion.
func rtpFrames(frames [][]byte) []*rtp.Packet {
	pk := rtp.NewPacketizer(1200, 96, 0x1234, &codecs.VP8Payloader{}, rtp.NewRandomSequencer(), 90000)
	var out []*rtp.Packet
	for _, f := range frames {
		out = append(out, pk.Packetize(f, 3000)...) // 30 fps
	}
	return out
}

func TestRecorderWritesPlayableWebM(t *testing.T) {
	// Межкадры до первого ключевого не пишутся: декодировать их нечем.
	frames := [][]byte{vp8Inter(500), vp8Inter(500), vp8Key(1365, 767, 5000)}
	for i := 0; i < 20; i++ {
		frames = append(frames, vp8Inter(800))
	}
	frames = append(frames, vp8Key(1365, 767, 5000), vp8Inter(10)) // последний остаётся в буфере сборки

	var buf bytes.Buffer
	r := NewRecorder(nopCloser{&buf}, 0)
	for _, p := range rtpFrames(frames) {
		if err := r.WriteRTP(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal("second Close:", err)
	}

	got := parseWebM(t, buf.Bytes())
	if got.codec != "V_VP8" || got.w != 1365 || got.h != 767 {
		t.Fatalf("track %q %dx%d", got.codec, got.w, got.h)
	}
	if len(got.blocks) != 22 || r.Frames() != 22 {
		t.Fatalf("blocks %d, frames %d, want 22", len(got.blocks), r.Frames())
	}
	if !got.blocks[0].Keyframe || got.blocks[1].Keyframe || !got.blocks[21].Keyframe {
		t.Fatal("keyframe flags wrong")
	}
	if !bytes.Equal(got.blocks[0].Data[0], frames[2]) {
		t.Fatal("first block payload differs from first keyframe")
	}
	if r.Truncated() {
		t.Fatal("truncated without quota")
	}
}

func TestRecorderQuotaTruncates(t *testing.T) {
	frames := [][]byte{vp8Key(640, 360, 4000)}
	for i := 0; i < 50; i++ {
		frames = append(frames, vp8Inter(1000))
	}
	const quota = 20000
	var buf bytes.Buffer
	r := NewRecorder(nopCloser{&buf}, quota)
	for _, p := range rtpFrames(frames) {
		if err := r.WriteRTP(p); err != nil {
			t.Fatal(err)
		}
	}
	if !r.Truncated() {
		t.Fatal("quota not enforced")
	}
	if r.Bytes() > quota || int64(buf.Len()) > quota {
		t.Fatalf("wrote %d bytes over quota %d", buf.Len(), quota)
	}
	// Обрезанный файл остаётся воспроизводимым.
	got := parseWebM(t, buf.Bytes())
	if len(got.blocks) == 0 || len(got.blocks) >= 50 {
		t.Fatalf("blocks %d", len(got.blocks))
	}
	_ = r.Close()
}

func TestVP8KeyframeInfo(t *testing.T) {
	key, w, h, ok := vp8KeyframeInfo(vp8Key(1365, 767, 20))
	if !ok || !key || w != 1365 || h != 767 {
		t.Fatalf("key: %v %d %d %v", key, w, h, ok)
	}
	if key, _, _, ok := vp8KeyframeInfo(vp8Inter(5)); !ok || key {
		t.Fatal("inter frame misread")
	}
	bad := vp8Key(10, 10, 20)
	bad[3] = 0
	if _, _, _, ok := vp8KeyframeInfo(bad); ok {
		t.Fatal("missing start code accepted")
	}
}
