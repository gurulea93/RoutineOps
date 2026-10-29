//go:build screen

package screen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// fakeScreen — захватчик с управляемой картинкой: BGRX, stride с запасом, как у DXGI.
type fakeScreen struct {
	w, h, stride int
	pix          []byte
	fail         error
}

func newFakeScreen(w, h int) *fakeScreen {
	return &fakeScreen{w: w, h: h, stride: w*4 + 16, pix: make([]byte, (w*4+16)*h)}
}

func (f *fakeScreen) Size() (int, int) { return f.w, f.h }
func (f *fakeScreen) Capture() ([]byte, int, error) {
	if f.fail != nil {
		return nil, 0, f.fail
	}
	return f.pix, f.stride, nil
}
func (f *fakeScreen) Close() error { return nil }

func (f *fakeScreen) paint(x, y int, b byte) { f.pix[y*f.stride+x*4] = b }

type fakeNotice struct{ closed bool }

func (n *fakeNotice) Close() { n.closed = true }

func TestFitSide(t *testing.T) {
	for _, c := range []struct{ w, h, max, ww, wh int }{
		{2560, 1600, 1280, 1280, 800},
		{1600, 2560, 1280, 800, 1280},
		{1024, 768, 1280, 1024, 768},
		{3000, 10, 1280, 1280, 4},
	} {
		if w, h := FitSide(c.w, c.h, c.max); w != c.ww || h != c.wh {
			t.Errorf("FitSide(%d,%d,%d) = %d,%d", c.w, c.h, c.max, w, h)
		}
	}
}

func TestProfileByName(t *testing.T) {
	if p, fb := ProfileByName("medium"); p.FPS != 2 || p.MaxSide != 1920 || fb {
		t.Fatal("medium")
	}
	if p, fb := ProfileByName("ultra"); p.Name != "low" || !fb {
		t.Fatal("unknown profile must fall back to low with flag")
	}
	if _, fb := ProfileByName(""); fb {
		t.Fatal("empty profile is the default, not a fallback")
	}
}

func TestTileEncoderSendsOnlyChangedTiles(t *testing.T) {
	scr := newFakeScreen(300, 200) // 3×2 тайла, крайние неполные
	enc := NewTileEncoder(scr.w, scr.h, ProfileLow)

	first, err := enc.Encode(scr.pix, scr.stride, false)
	if err != nil || len(first) != 6 {
		t.Fatalf("first frame must be full: %d tiles, %v", len(first), err)
	}
	if last := first[5]; last.X != 256 || last.Y != 128 || last.W != 44 || last.H != 72 {
		t.Fatalf("edge tile %+v", last)
	}
	same, _ := enc.Encode(scr.pix, scr.stride, false)
	if len(same) != 0 {
		t.Fatalf("static screen produced %d tiles", len(same))
	}
	scr.paint(150, 150, 0xff) // тайл (128,128)
	diff, _ := enc.Encode(scr.pix, scr.stride, false)
	if len(diff) != 1 || diff[0].X != 128 || diff[0].Y != 128 {
		t.Fatalf("changed tiles %+v", diff)
	}
	key, _ := enc.Encode(scr.pix, scr.stride, true)
	if len(key) != 6 {
		t.Fatalf("keyframe %d tiles", len(key))
	}
	for _, tl := range key {
		if !bytes.HasPrefix(tl.JPEG, []byte{0xff, 0xd8}) {
			t.Fatal("tile is not JPEG")
		}
	}
}

func TestTileEncoderScales(t *testing.T) {
	scr := newFakeScreen(2560, 1440)
	enc := NewTileEncoder(scr.w, scr.h, ProfileLow)
	if enc.W != 1280 || enc.H != 720 {
		t.Fatalf("geometry %dx%d", enc.W, enc.H)
	}
	if _, err := enc.Encode(scr.pix, scr.stride, true); err != nil {
		t.Fatal(err)
	}
}

func TestWireRoundTripAndLimits(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrameMsg(&buf, wireFrame{Seq: 9, Keyframe: true, Last: true, CapturedMs: 1234, Payload: []byte("rsf")}); err != nil {
		t.Fatal(err)
	}
	typ, body, err := readMsg(&buf)
	if err != nil || typ != msgFrame {
		t.Fatal(typ, err)
	}
	f, err := parseFrameMsg(body)
	if err != nil || f.Seq != 9 || !f.Keyframe || !f.Last || f.CapturedMs != 1234 || string(f.Payload) != "rsf" {
		t.Fatalf("%+v %v", f, err)
	}
	// Заявленная длина больше потолка — отказ без аллокации.
	if _, _, err := readMsg(bytes.NewReader([]byte{msgFrame, 0xff, 0xff, 0xff, 0xff})); !errors.Is(err, errWire) {
		t.Fatalf("huge length: %v", err)
	}
	if err := writeMsg(io.Discard, msgFrame, make([]byte, maxWireMsg+1)); !errors.Is(err, errWire) {
		t.Fatalf("oversize write: %v", err)
	}
}

// workerOut разбирает всё, что worker написал службе.
type workerOut struct {
	hello  *wireHello
	frames []wireFrame
	end    *wireEnd
}

func parseWorkerOut(t *testing.T, b []byte) workerOut {
	t.Helper()
	var o workerOut
	r := bytes.NewReader(b)
	for r.Len() > 0 {
		typ, body, err := readMsg(r)
		if err != nil {
			t.Fatal(err)
		}
		switch typ {
		case msgHello:
			o.hello = &wireHello{}
			_ = json.Unmarshal(body, o.hello)
		case msgFrame:
			f, err := parseFrameMsg(body)
			if err != nil {
				t.Fatal(err)
			}
			f.Payload = bytes.Clone(f.Payload)
			o.frames = append(o.frames, f)
		case msgEnd:
			o.end = &wireEnd{}
			_ = json.Unmarshal(body, o.end)
		}
	}
	return o
}

func TestWorkerNoNoticeNoFrames(t *testing.T) {
	var out bytes.Buffer
	got := RunWorker(context.Background(), WorkerConfig{
		NewCapturer: func() (Capturer, error) { return newFakeScreen(64, 64), nil },
		ShowNotice:  func(_, _ string) (Notice, error) { return nil, errors.New("no x") },
	}, bytes.NewReader(nil), &out)
	o := parseWorkerOut(t, out.Bytes())
	if got != ReasonNoticeUnavailable || o.hello != nil || len(o.frames) != 0 || o.end == nil || o.end.Reason != "NOTICE_UNAVAILABLE" {
		t.Fatalf("reason %s, out %+v", got, o)
	}
}

func TestWorkerCaptureRefusalKeepsCode(t *testing.T) {
	var out bytes.Buffer
	got := RunWorker(context.Background(), WorkerConfig{
		NewCapturer: func() (Capturer, error) { return nil, reasonErr(ReasonWaylandUnsupported, nil) },
		ShowNotice:  func(_, _ string) (Notice, error) { t.Fatal("notice before capture"); return nil, nil },
	}, bytes.NewReader(nil), &out)
	if got != ReasonWaylandUnsupported {
		t.Fatalf("reason %s", got)
	}
}

func TestWorkerStreamsUntilStop(t *testing.T) {
	scr := newFakeScreen(320, 200)
	notice := &fakeNotice{}
	inR, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan EndReason, 1)
	go func() {
		done <- RunWorker(context.Background(), WorkerConfig{
			Profile:     "medium",
			NewCapturer: func() (Capturer, error) { return scr, nil },
			ShowNotice:  func(_, _ string) (Notice, error) { return notice, nil },
		}, inR, &out)
	}()
	time.Sleep(1300 * time.Millisecond) // medium = 2 к/с → 2–3 кадра
	if err := writeJSONMsg(inW, msgStop, wireStop{Reason: "VIEWER_GONE"}); err != nil {
		t.Fatal(err)
	}
	var got EndReason
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	if got != "VIEWER_GONE" || !notice.closed {
		t.Fatalf("reason %s, notice closed %v", got, notice.closed)
	}
	o := parseWorkerOut(t, out.Bytes())
	if o.hello == nil || o.hello.Width != 320 || o.hello.Profile != "medium" || len(o.frames) < 2 {
		t.Fatalf("hello %+v, frames %d", o.hello, len(o.frames))
	}
	if !o.frames[0].Keyframe || o.frames[1].Keyframe {
		t.Fatal("first frame must be key, second a delta")
	}
	for _, f := range o.frames {
		p, err := screenframe.ParsePart(f.Payload)
		if err != nil || p.Seq != f.Seq || p.Keyframe != f.Keyframe || p.Last != f.Last {
			t.Fatalf("frame %d: %+v %v", f.Seq, p, err)
		}
	}
	if o.end == nil || o.end.Reason != "VIEWER_GONE" {
		t.Fatalf("end %+v", o.end)
	}
}

func TestWorkerCaptureFailureEnds(t *testing.T) {
	scr := newFakeScreen(64, 64)
	scr.fail = errors.New("x gone")
	var out bytes.Buffer
	inR, _ := io.Pipe()
	got := RunWorker(context.Background(), WorkerConfig{
		NewCapturer: func() (Capturer, error) { return scr, nil },
		ShowNotice:  func(_, _ string) (Notice, error) { return &fakeNotice{}, nil },
	}, inR, &out)
	if got != ReasonCaptureFailed {
		t.Fatalf("reason %s", got)
	}
}
