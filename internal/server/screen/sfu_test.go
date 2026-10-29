//go:build screen

package screen

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	pb "github.com/Floodww/RoutineOps/proto"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// relayFixture — сеанс relay с подключённым агентом.
type relayFixture struct {
	s     *RelaySession
	agent *fakeAgent
	rec   *Recorder
	buf   *bytes.Buffer
	mu    sync.Mutex
	sent  []*pb.SessionSignal
}

func newRelayFixture(t *testing.T, opts SessionOptions) *relayFixture {
	t.Helper()
	f := &relayFixture{agent: newFakeAgent(t), buf: &bytes.Buffer{}}
	f.rec = NewRecorder(nopCloser{f.buf}, 0)
	opts.Recorder = f.rec
	if opts.KeyframeMinInterval == 0 {
		opts.KeyframeMinInterval = 10 * time.Millisecond
	}
	opts.SendToAgent = func(sig *pb.SessionSignal) error {
		f.mu.Lock()
		f.sent = append(f.sent, sig)
		f.mu.Unlock()
		switch m := sig.GetMsg().(type) {
		case *pb.SessionSignal_SdpAnswer:
			return setAnswer(f.agent.pc, m.SdpAnswer)
		case *pb.SessionSignal_KeyframeRequest:
			f.agent.requestKey()
		}
		return nil
	}
	s, err := newTestRTC(t).NewRelaySession("s1", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close("TEST_DONE") })
	f.s = s

	offer := gatheredOffer(t, f.agent.pc)
	if err := s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: offer}}); err != nil {
		t.Fatal(err)
	}
	go f.agent.stream()
	return f
}

func (f *relayFixture) addViewer(t *testing.T, id string) *fakeViewer {
	t.Helper()
	v := newFakeViewer(t)
	ans, err := f.s.AddViewer(context.Background(), id, gatheredOffer(t, v.pc))
	if err != nil {
		t.Fatal(err)
	}
	if err := setAnswer(v.pc, ans); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRelayForwardsAgentVideoToViewers(t *testing.T) {
	f := newRelayFixture(t, SessionOptions{})
	v1 := f.addViewer(t, "op1")
	v2 := f.addViewer(t, "op2")
	v1.waitMedia(t)
	v2.waitMedia(t)

	if !strings.EqualFold(v1.mime, webrtc.MimeTypeVP8) {
		t.Fatalf("viewer codec %q", v1.mime)
	}
	if f.s.Viewers() != 2 {
		t.Fatalf("viewers = %d", f.s.Viewers())
	}
	// Подключение зрителя → keyframe_request агенту (и PLI по RTCP).
	eventually(t, "keyframe request + PLI on viewer join", func() bool {
		kf, pli := f.agent.counts()
		return kf >= 1 && pli >= 1
	})
}

func TestRelayViewerPLIBecomesKeyframeRequest(t *testing.T) {
	f := newRelayFixture(t, SessionOptions{})
	v := f.addViewer(t, "op1")
	v.waitMedia(t)
	time.Sleep(50 * time.Millisecond) // пережить интервал после запроса на подключение
	before, _ := f.agent.counts()

	v.mu.Lock()
	ssrc := v.ssrc
	v.mu.Unlock()
	if err := v.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "keyframe_request after viewer PLI", func() bool {
		kf, _ := f.agent.counts()
		return kf > before
	})
}

func TestRelayRecordsWebM(t *testing.T) {
	f := newRelayFixture(t, SessionOptions{})
	v := f.addViewer(t, "op1")
	v.waitMedia(t)
	eventually(t, "recorded frames", func() bool { return f.rec.Frames() >= 10 })
	f.s.Close("OPERATOR_REVOKED")

	rec := parseWebM(t, f.buf.Bytes())
	if rec.codec != "V_VP8" || rec.w != 640 || rec.h != 360 {
		t.Fatalf("track %q %dx%d", rec.codec, rec.w, rec.h)
	}
	if len(rec.blocks) < 10 || !rec.blocks[0].Keyframe {
		t.Fatalf("blocks=%d, first key=%v", len(rec.blocks), len(rec.blocks) > 0 && rec.blocks[0].Keyframe)
	}
}

func TestRelayCloseTearsDown(t *testing.T) {
	var reason string
	f := newRelayFixture(t, SessionOptions{OnClose: func(r string) { reason = r }})
	v := f.addViewer(t, "op1")
	v.waitMedia(t)

	f.s.Close("OPERATOR_REVOKED")
	f.s.Close("SECOND") // повторный — без эффекта
	select {
	case <-f.s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if reason != "OPERATOR_REVOKED" || f.s.EndReason() != "OPERATOR_REVOKED" {
		t.Fatalf("reason %q / %q", reason, f.s.EndReason())
	}
	if f.s.Viewers() != 0 {
		t.Fatalf("viewers after close: %d", f.s.Viewers())
	}
	if _, err := f.s.AddViewer(context.Background(), "op2", "v=0"); !errors.Is(err, errSessionClosed) {
		t.Fatalf("AddViewer after close: %v", err)
	}
	eventually(t, "agent pc closed", func() bool {
		st := f.agent.pc.ConnectionState()
		return st == webrtc.PeerConnectionStateClosed || st == webrtc.PeerConnectionStateFailed ||
			st == webrtc.PeerConnectionStateDisconnected
	})
}

func TestRelayViewerLimitAndReplace(t *testing.T) {
	f := newRelayFixture(t, SessionOptions{MaxViewers: 1})
	f.addViewer(t, "op1")

	v2 := newFakeViewer(t)
	if _, err := f.s.AddViewer(context.Background(), "op2", gatheredOffer(t, v2.pc)); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("second viewer: %v", err)
	}
	// Перезагрузка вкладки тем же оператором — замена, а не отказ.
	v1b := f.addViewer(t, "op1")
	v1b.waitMedia(t)
	if f.s.Viewers() != 1 {
		t.Fatalf("viewers = %d", f.s.Viewers())
	}
}

func TestRelaySignalProtocol(t *testing.T) {
	r := newTestRTC(t)
	s, err := r.NewRelaySession("p", SessionOptions{SendToAgent: func(*pb.SessionSignal) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close("TEST_DONE")

	ice := &pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{Candidate: "candidate:1 1 udp 1 127.0.0.1 9 typ host"}}}
	if err := s.HandleAgentSignal(ice); !errors.Is(err, errProtocol) {
		t.Fatalf("ice before offer: %v", err)
	}
	if err := s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpAnswer{SdpAnswer: "x"}}); !errors.Is(err, errProtocol) {
		t.Fatalf("answer from agent: %v", err)
	}
	if err := s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: "garbage"}}); !errors.Is(err, errProtocol) {
		t.Fatalf("garbage offer: %v", err)
	}
	if _, err := r.NewRelaySession("x", SessionOptions{}); err == nil {
		t.Fatal("session without SendToAgent accepted")
	}
}

func TestRelayRejectsSecondOffer(t *testing.T) {
	f := newRelayFixture(t, SessionOptions{})
	other := newFakeAgent(t)
	err := f.s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: gatheredOffer(t, other.pc)}})
	if !errors.Is(err, errProtocol) {
		t.Fatalf("second offer: %v", err)
	}
}

// Оферта без VP8 (только H.264) — сеанс умирает кодом, а не ждёт кадров, которых не будет.
func TestRelayRejectsNonVP8Offer(t *testing.T) {
	// Клиент с ОДНИМ H.264: стандартный набор кодеков pion положил бы в оферту и VP8.
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	pc := newPC(t, webrtc.NewAPI(webrtc.WithMediaEngine(me)))
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	s, err := newTestRTC(t).NewRelaySession("h", SessionOptions{SendToAgent: func(*pb.SessionSignal) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	err = s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: gatheredOffer(t, pc)}})
	if !errors.Is(err, errProtocol) {
		t.Fatalf("h264-only offer: %v", err)
	}
	if r := s.EndReason(); r != "CODEC_UNSUPPORTED" {
		t.Fatalf("end reason %q", r)
	}
}

// Сервер за NAT/в docker объявляет публичный адрес, а не внутренний.
func TestRTCPublicIPsInAnswer(t *testing.T) {
	if _, err := NewRTC(RTCConfig{UDPAddr: "127.0.0.1:0", PublicIPs: []string{"rops.example.com"}}); err == nil {
		t.Fatal("hostname accepted as public ip")
	}
	r, err := NewRTC(RTCConfig{UDPAddr: "127.0.0.1:0", IncludeLoopback: true, PublicIPs: []string{"203.0.113.7"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s, err := r.NewRelaySession("n", SessionOptions{SendToAgent: func(*pb.SessionSignal) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close("TEST_DONE")
	v := newFakeViewer(t)
	ans, err := s.AddViewer(context.Background(), "op1", gatheredOffer(t, v.pc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans, "203.0.113.7") {
		t.Fatalf("answer has no public ip candidate:\n%s", ans)
	}
}
