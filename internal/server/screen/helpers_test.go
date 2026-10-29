//go:build screen

package screen

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func newTestRTC(t *testing.T) *RTC {
	t.Helper()
	r, err := NewRTC(RTCConfig{UDPAddr: "127.0.0.1:0", IncludeLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// clientAPI — «агент» и «браузер» тестов: обычный pion, только loopback.
func clientAPI(t *testing.T) *webrtc.API {
	t.Helper()
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se))
}

func newPC(t *testing.T, api *webrtc.API) *webrtc.PeerConnection {
	t.Helper()
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// gatheredOffer — оферта с уже собранными кандидатами.
func gatheredOffer(t *testing.T, pc *webrtc.PeerConnection) string {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	return pc.LocalDescription().SDP
}

func gatheredAnswer(t *testing.T, pc *webrtc.PeerConnection, offer string) string {
	t.Helper()
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		t.Fatal(err)
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(ans); err != nil {
		t.Fatal(err)
	}
	<-done
	return pc.LocalDescription().SDP
}

func setAnswer(pc *webrtc.PeerConnection, sdp string) error {
	return pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// vp8Key/vp8Inter — синтетические кадры VP8: корректный заголовок, остальное — заполнитель.
// SFU и запись не декодируют поток, им нужен только заголовок.
func vp8Key(w, h, size int) []byte {
	b := make([]byte, size)
	copy(b, []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, byte(w), byte(w >> 8), byte(h), byte(h >> 8)})
	for i := 10; i < size; i++ {
		b[i] = byte(i)
	}
	return b
}

func vp8Inter(size int) []byte {
	b := make([]byte, size)
	b[0], b[1] = 0x31, 0x02
	for i := 3; i < size; i++ {
		b[i] = byte(i * 7)
	}
	return b
}

// fakeAgent — сторона агента: VP8-дорожка, ключевой кадр по запросу.
type fakeAgent struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticSample
	sender  *webrtc.RTPSender
	mu      sync.Mutex
	wantKey bool
	plis    int
	kfReqs  int
	stop    chan struct{}
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	pc := newPC(t, clientAPI(t))
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "agent")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{pc: pc, track: track, sender: tr.Sender(), wantKey: true, stop: make(chan struct{})}
	t.Cleanup(func() { close(a.stop) })
	go func() {
		for {
			pkts, _, err := a.sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				if _, ok := p.(*rtcp.PictureLossIndication); ok {
					a.mu.Lock()
					a.plis++
					a.mu.Unlock()
				}
			}
		}
	}()
	return a
}

func (a *fakeAgent) requestKey() {
	a.mu.Lock()
	a.wantKey = true
	a.kfReqs++
	a.mu.Unlock()
}

func (a *fakeAgent) counts() (kfReqs, plis int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kfReqs, a.plis
}

// stream — 30 кадров/с: ключевой при запросе, иначе межкадр.
func (a *fakeAgent) stream() {
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-tick.C:
		}
		a.mu.Lock()
		key := a.wantKey
		a.wantKey = false
		a.mu.Unlock()
		frame := vp8Inter(600)
		if key {
			frame = vp8Key(640, 360, 3000)
		}
		_ = a.track.WriteSample(media.Sample{Data: frame, Duration: 33 * time.Millisecond})
	}
}

// fakeViewer — браузер: recvonly-видео, счёт пакетов.
type fakeViewer struct {
	pc      *webrtc.PeerConnection
	mu      sync.Mutex
	packets int
	mime    string
	ssrc    webrtc.SSRC
	got     chan struct{}
	gotOnce sync.Once
}

func newFakeViewer(t *testing.T) *fakeViewer {
	t.Helper()
	v := &fakeViewer{pc: newPC(t, clientAPI(t)), got: make(chan struct{})}
	if _, err := v.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	v.pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		v.mu.Lock()
		v.mime, v.ssrc = tr.Codec().MimeType, tr.SSRC()
		v.mu.Unlock()
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			v.mu.Lock()
			v.packets++
			n := v.packets
			v.mu.Unlock()
			if n >= 20 {
				v.gotOnce.Do(func() { close(v.got) })
			}
		}
	})
	return v
}

func (v *fakeViewer) waitMedia(t *testing.T) {
	t.Helper()
	select {
	case <-v.got:
	case <-time.After(15 * time.Second):
		v.mu.Lock()
		defer v.mu.Unlock()
		t.Fatalf("viewer got %d packets, want >= 20", v.packets)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
