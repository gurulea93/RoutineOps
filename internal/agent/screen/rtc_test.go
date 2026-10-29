//go:build screen

package screen

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	srv "github.com/Floodww/RoutineOps/internal/server/screen"
	pb "github.com/Floodww/RoutineOps/proto"
)

// ---- фейки: захват без X-сервера и кодер без libvpx ----

type fakeCapturer struct {
	w, h   int
	n      atomic.Int32
	failAt int32 // >0 — ошибка на этом кадре
	closed atomic.Bool
	pix    []byte
}

func newFakeCapturer(w, h int) *fakeCapturer {
	return &fakeCapturer{w: w, h: h, pix: make([]byte, 4*w*h)}
}

func (c *fakeCapturer) Size() (int, int) { return c.w, c.h }

func (c *fakeCapturer) Capture() ([]byte, int, error) {
	n := c.n.Add(1)
	if c.failAt > 0 && n >= c.failAt {
		return nil, 0, errors.New("display went away")
	}
	for i := range c.pix {
		c.pix[i] = byte(int(n) + i)
	}
	return c.pix, 4 * c.w, nil
}

func (c *fakeCapturer) Close() error { c.closed.Store(true); return nil }

type fakeEncoder struct {
	cfg      EncoderConfig
	mu       sync.Mutex
	calls    int
	keys     int
	bitrates []int
	closed   bool
}

func (e *fakeEncoder) Encode(f *I420, forceKey bool) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("encode after close")
	}
	if f.Width != e.cfg.Width || f.Height != e.cfg.Height {
		return nil, errors.New("geometry mismatch")
	}
	e.calls++
	if forceKey || e.calls == 1 {
		e.keys++
		w, h := e.cfg.Width, e.cfg.Height
		b := make([]byte, 2000)
		copy(b, []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, byte(w), byte(w >> 8), byte(h), byte(h >> 8)})
		return b, nil
	}
	b := make([]byte, 300)
	b[0] = 0x31
	return b, nil
}

func (e *fakeEncoder) SetBitrate(kbps int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bitrates = append(e.bitrates, kbps)
	return nil
}

func (e *fakeEncoder) Close() { e.mu.Lock(); e.closed = true; e.mu.Unlock() }

func (e *fakeEncoder) snapshot() (calls, keys int, bitrates []int, closed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, e.keys, append([]int(nil), e.bitrates...), e.closed
}

func loopbackSE() *webrtc.SettingEngine {
	se := &webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	return se
}

func browserPC(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(*loopbackSE())).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// browser — зритель: считает RTP-пакеты, помнит SSRC для PLI/REMB.
type browser struct {
	pc      *webrtc.PeerConnection
	packets atomic.Int32
	ssrc    atomic.Uint32
}

func newBrowser(t *testing.T) *browser {
	b := &browser{pc: browserPC(t)}
	b.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		b.ssrc.Store(uint32(tr.SSRC()))
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			b.packets.Add(1)
		}
	})
	return b
}

func gather(t *testing.T, pc *webrtc.PeerConnection, desc webrtc.SessionDescription, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(desc); err != nil {
		t.Fatal(err)
	}
	<-done
	return pc.LocalDescription().SDP
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type pipeline struct {
	cap *fakeCapturer
	enc *fakeEncoder
	st  *Streamer
}

func newPipeline(t *testing.T, w, h int, send func(*pb.SessionSignal) error, mod func(*StreamerConfig)) *pipeline {
	t.Helper()
	p := &pipeline{cap: newFakeCapturer(w, h)}
	cfg := StreamerConfig{
		FPS: 30, SendSignal: send, SettingEngine: loopbackSE(),
		NewEncoder: func(ec EncoderConfig) (Encoder, error) { p.enc = &fakeEncoder{cfg: ec}; return p.enc, nil },
	}
	if mod != nil {
		mod(&cfg)
	}
	st, err := NewStreamer(p.cap, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	p.st = st
	return p
}

// ---- сквозной тест: агент → SFU сервера (B2) → браузер ----

func TestStreamerRelayEndToEnd(t *testing.T) {
	rtc, err := srv.NewRTC(srv.RTCConfig{UDPAddr: "127.0.0.1:0", IncludeLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rtc.Close()
	rec := srv.NewRecorder(nopWC{}, 0)

	var p *pipeline
	var kfRequests atomic.Int32
	sess, err := rtc.NewRelaySession("e2e", srv.SessionOptions{
		Recorder:            rec,
		KeyframeMinInterval: 10 * time.Millisecond,
		SendToAgent: func(sig *pb.SessionSignal) error {
			if sig.GetKeyframeRequest() {
				kfRequests.Add(1)
			}
			return p.st.HandleSignal(sig) // SessionDown.signal
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close("TEST_DONE")

	// Экран 1920×1080 → потолок low 1280×800 → 1280×720.
	p = newPipeline(t, 1920, 1080, sess.HandleAgentSignal, nil)
	if w, h := p.st.Geometry(); w != 1280 || h != 720 {
		t.Fatalf("geometry %dx%d", w, h)
	}
	if err := p.st.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	b := newBrowser(t)
	if _, err := b.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := b.pc.CreateOffer(nil)
	ans, err := sess.AddViewer(context.Background(), "op1", gather(t, b.pc, offer, err))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "browser receives video", func() bool { return b.packets.Load() >= 30 })
	waitFor(t, "server records frames", func() bool { return rec.Frames() >= 5 })
	waitFor(t, "keyframe_request on viewer join", func() bool { return kfRequests.Load() >= 1 })

	// PLI браузера → keyframe_request сервера → ключевой кадр кодера.
	time.Sleep(50 * time.Millisecond)
	_, keysBefore, _, _ := p.enc.snapshot()
	if err := b.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: b.ssrc.Load()}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "keyframe after browser PLI", func() bool {
		_, keys, _, _ := p.enc.snapshot()
		return keys > keysBefore
	})
	if st := p.st.Stats(); st.Frames == 0 || st.Bytes == 0 {
		t.Fatalf("stats %+v", st)
	}

	p.st.Close()
	<-p.st.Done()
	var re *ReasonError
	if !errors.As(p.st.Err(), &re) || re.Reason != ReasonStopped {
		t.Fatalf("end reason %v", p.st.Err())
	}
	if _, _, _, closed := p.enc.snapshot(); !closed || !p.cap.closed.Load() {
		t.Fatal("encoder/capturer not closed")
	}
	// Сервер видит уход агента и закрывает сеанс.
	select {
	case <-sess.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("server session survived agent close")
	}
}

type nopWC struct{}

func (nopWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopWC) Close() error                { return nil }

// ---- P2P: агент → браузер напрямую, сервер только брокер; REMB браузера → битрейт ----

func TestStreamerP2PEndToEnd(t *testing.T) {
	var p *pipeline
	sess, err := srv.NewP2PSession("p2p", func(sig *pb.SessionSignal) error { return p.st.HandleSignal(sig) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close("TEST_DONE")
	p = newPipeline(t, 800, 600, sess.HandleAgentSignal, func(c *StreamerConfig) { c.BitrateKbps = 1000 })
	if err := p.st.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	offer, err := sess.AgentOffer(context.Background(), "op1")
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t)
	if err := b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		t.Fatal(err)
	}
	answer, err := b.pc.CreateAnswer(nil)
	if err := sess.ViewerAnswer("op1", gather(t, b.pc, answer, err)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "browser receives video", func() bool { return b.packets.Load() >= 30 })

	// REMB 400 кбит/с → кодер 400; REMB выше потолка → не выше потолка 1000.
	if err := b.pc.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverEstimatedMaximumBitrate{Bitrate: 400_000, SSRCs: []uint32{b.ssrc.Load()}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bitrate lowered by REMB", func() bool {
		_, _, br, _ := p.enc.snapshot()
		return len(br) > 0 && br[len(br)-1] == 400
	})
	if err := b.pc.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverEstimatedMaximumBitrate{Bitrate: 5_000_000, SSRCs: []uint32{b.ssrc.Load()}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bitrate capped at profile ceiling", func() bool {
		_, _, br, _ := p.enc.snapshot()
		return len(br) > 0 && br[len(br)-1] == 1000
	})
}

// ---- отказы с кодами ----

func TestStreamerCaptureFailureEndsWithReason(t *testing.T) {
	rtc, err := srv.NewRTC(srv.RTCConfig{UDPAddr: "127.0.0.1:0", IncludeLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rtc.Close()
	var p *pipeline
	sess, err := rtc.NewRelaySession("f", srv.SessionOptions{SendToAgent: func(sig *pb.SessionSignal) error { return p.st.HandleSignal(sig) }})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close("TEST_DONE")
	p = newPipeline(t, 640, 480, sess.HandleAgentSignal, nil)
	p.cap.failAt = 5
	if err := p.st.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.st.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("streamer did not stop on capture failure")
	}
	var re *ReasonError
	if !errors.As(p.st.Err(), &re) || re.Reason != ReasonCaptureFailed {
		t.Fatalf("reason %v", p.st.Err())
	}
}

func TestStreamerRefusesUnavailableCodec(t *testing.T) {
	send := func(*pb.SessionSignal) error { return nil }
	var re *ReasonError

	_, err := NewStreamer(newFakeCapturer(64, 64), StreamerConfig{Codec: "h264", SendSignal: send})
	if !errors.As(err, &re) || re.Reason != ReasonCodecUnavailable {
		t.Fatalf("h264: %v", err)
	}
	// Реальный путь сборки без libvpx: кодер не зарегистрирован → CODEC_UNAVAILABLE, а не паника.
	_, err = NewStreamer(newFakeCapturer(64, 64), StreamerConfig{SendSignal: send,
		NewEncoder: func(EncoderConfig) (Encoder, error) { return nil, ErrCodecUnavailable }})
	if !errors.As(err, &re) || re.Reason != ReasonCodecUnavailable {
		t.Fatalf("no vpx: %v", err)
	}
}

func TestStreamerSignalProtocol(t *testing.T) {
	p := newPipeline(t, 64, 64, func(*pb.SessionSignal) error { return nil }, nil)
	ice := &pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{Candidate: "c"}}}
	if err := p.st.HandleSignal(ice); err == nil {
		t.Fatal("ice before answer accepted")
	}
	var re *ReasonError
	if !errors.As(p.st.Err(), &re) || re.Reason != ReasonSignalFailed {
		t.Fatalf("reason %v", p.st.Err())
	}

	p2 := newPipeline(t, 64, 64, func(*pb.SessionSignal) error { return nil }, nil)
	if err := p2.st.HandleSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: "x"}}); err == nil {
		t.Fatal("offer from server accepted")
	}
}

// ---- чистые функции ----

func TestFitGeometry(t *testing.T) {
	cases := []struct{ w, h, mw, mh, ww, wh int }{
		{1920, 1080, 1280, 800, 1280, 720},
		{1280, 1024, 1280, 800, 1000, 800},
		{1024, 768, 1280, 800, 1024, 768}, // меньше потолка — не трогаем
		{1365, 767, 1280, 800, 1280, 718}, // чётная геометрия
		{3840, 2160, 1280, 800, 1280, 720},
		{0, 100, 1280, 800, 0, 0},
	}
	for _, c := range cases {
		if w, h := FitGeometry(c.w, c.h, c.mw, c.mh); w != c.ww || h != c.wh {
			t.Errorf("Fit(%dx%d in %dx%d) = %dx%d, want %dx%d", c.w, c.h, c.mw, c.mh, w, h, c.ww, c.wh)
		}
	}
}

func TestX11Availability(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cases := []struct {
		name string
		env  map[string]string
		want EndReason
		ok   bool
	}{
		{"x11", map[string]string{"DISPLAY": ":0", "XDG_SESSION_TYPE": "x11"}, "", true},
		{"no session", map[string]string{}, ReasonNoConsoleUser, false},
		{"wayland without xwayland", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, ReasonWaylandUnsupported, false},
		// XWayland отвечает на DISPLAY, но показал бы только X-окна — отказ, а не чёрный экран.
		{"wayland with xwayland", map[string]string{"DISPLAY": ":0", "XDG_SESSION_TYPE": "wayland"}, ReasonWaylandUnsupported, false},
	}
	for _, c := range cases {
		if r, ok := x11Availability(env(c.env)); r != c.want || ok != c.ok {
			t.Errorf("%s: %q,%v", c.name, r, ok)
		}
	}
}

func TestI420Downscale(t *testing.T) {
	// 4×2 источник: левая половина чёрная, правая белая → 2×1 кадр: чёрный и белый пиксель.
	const sw, sh = 4, 2
	pix := make([]byte, 4*sw*sh)
	for y := 0; y < sh; y++ {
		for x := 2; x < sw; x++ {
			p := pix[4*(y*sw+x):]
			p[0], p[1], p[2] = 255, 255, 255
		}
	}
	f := NewI420(2, 1)
	if err := f.FromBGRAScaled(pix, 4*sw, sw, sh); err != nil {
		t.Fatal(err)
	}
	if f.Y[0] != 16 || f.Y[1] != 235 {
		t.Fatalf("Y = %v", f.Y)
	}
	if err := NewI420(8, 8).FromBGRAScaled(pix, 4*sw, sw, sh); err == nil {
		t.Fatal("upscale accepted")
	}
}
