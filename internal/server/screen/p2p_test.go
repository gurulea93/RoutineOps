//go:build screen

package screen

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestParsePolicy(t *testing.T) {
	cases := []struct {
		in    string
		want  Policy
		known bool
	}{
		{"", PolicyRelayOnly, true},
		{"relay_only", PolicyRelayOnly, true},
		{" p2p_preferred ", PolicyP2PPreferred, true},
		{"p2p", PolicyRelayOnly, false}, // опечатка в настройке → режим С записью
	}
	for _, c := range cases {
		got, known := ParsePolicy(c.in)
		if got != c.want || known != c.known {
			t.Errorf("ParsePolicy(%q) = %q,%v; want %q,%v", c.in, got, known, c.want, c.known)
		}
	}
}

func TestChooseTransport(t *testing.T) {
	webrtcAgent := []string{"screen", "screen_webrtc_vp8"}
	cases := []struct {
		name      string
		agent     []string
		browser   []string
		policy    Policy
		recording bool
		want      Choice
	}{
		{"old agent → rsf1", []string{"screen"}, []string{"vp8"}, PolicyP2PPreferred, false,
			Choice{Transport: "rsf1", Mode: ModeRSF1, Record: true}},
		{"browser without vp8 → rsf1", webrtcAgent, nil, PolicyRelayOnly, false,
			Choice{Transport: "rsf1", Mode: ModeRSF1, Record: true}},
		{"relay_only", webrtcAgent, []string{"VP8"}, PolicyRelayOnly, false,
			Choice{Transport: "webrtc", VideoCodec: "vp8", Mode: ModeRelay, Record: true}},
		{"p2p allowed", webrtcAgent, []string{"vp8"}, PolicyP2PPreferred, false,
			Choice{Transport: "webrtc", VideoCodec: "vp8", Mode: ModeP2P, Record: false}},
		{"p2p but recording required → relay", webrtcAgent, []string{"vp8"}, PolicyP2PPreferred, true,
			Choice{Transport: "webrtc", VideoCodec: "vp8", Mode: ModeRelay, Record: true}},
	}
	for _, c := range cases {
		if got := ChooseTransport(c.agent, c.browser, c.policy, c.recording); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// P2P: сервер только передаёт SDP, медиа идут агент → браузер напрямую.
func TestP2PBrokersDirectConnection(t *testing.T) {
	agent := newFakeAgent(t)
	s, err := NewP2PSession("p", func(sig *pb.SessionSignal) error {
		if m, ok := sig.GetMsg().(*pb.SessionSignal_SdpAnswer); ok {
			return setAnswer(agent.pc, m.SdpAnswer)
		}
		t.Errorf("unexpected signal to agent: %T", sig.GetMsg())
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close("TEST_DONE")

	// Браузер может прийти раньше оферты агента — ждёт её.
	viewer := newFakeViewer(t)
	offerCh := make(chan string, 1)
	go func() {
		o, err := s.AgentOffer(context.Background(), "op1")
		if err != nil {
			t.Error(err)
		}
		offerCh <- o
	}()
	time.Sleep(50 * time.Millisecond)

	if err := s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: gatheredOffer(t, agent.pc)}}); err != nil {
		t.Fatal(err)
	}
	offer := <-offerCh
	// fakeViewer создан со своим recvonly-трансивером; ответ на оферту агента он сопоставит с ним.
	if err := s.ViewerAnswer("op1", gatheredAnswer(t, viewer.pc, offer)); err != nil {
		t.Fatal(err)
	}
	go agent.stream()
	viewer.waitMedia(t)

	if _, err := s.AgentOffer(context.Background(), "op2"); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("second viewer: %v", err)
	}
	if err := s.ViewerAnswer("op1", offer); !errors.Is(err, errProtocol) {
		t.Fatalf("second answer: %v", err)
	}
}

func TestP2PSignalProtocol(t *testing.T) {
	s, err := NewP2PSession("p", func(*pb.SessionSignal) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ice := &pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{Candidate: "c"}}}
	if err := s.HandleAgentSignal(ice); !errors.Is(err, errProtocol) {
		t.Fatalf("trickle in p2p: %v", err)
	}
	if err := s.HandleAgentSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: "garbage"}}); !errors.Is(err, errProtocol) {
		t.Fatalf("garbage offer: %v", err)
	}
	if err := s.ViewerAnswer("op1", "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n"); !errors.Is(err, errProtocol) {
		t.Fatalf("answer before taking offer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.AgentOffer(ctx, "op1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("offer wait: %v", err)
	}

	var reason string
	s.onClose = func(r string) { reason = r }
	s.Close("VIEWER_GONE")
	if _, err := s.AgentOffer(context.Background(), "op1"); !errors.Is(err, errSessionClosed) {
		t.Fatalf("offer after close: %v", err)
	}
	if reason != "VIEWER_GONE" {
		t.Fatalf("reason %q", reason)
	}
}

func TestICEServersFor(t *testing.T) {
	r, err := NewRTC(RTCConfig{UDPAddr: "127.0.0.1:0", P2PICEServers: []webrtc.ICEServer{
		{URLs: []string{"stun:stun.example.com:3478"}},
		{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "p"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.ICEServersFor(ModeRelay); len(got) != 0 {
		t.Fatalf("relay got ice servers: %v", got)
	}
	got := r.ICEServersFor(ModeP2P)
	if len(got) != 2 || got[1].GetUsername() != "u" || got[1].GetCredential() != "p" || got[0].GetUrls()[0] != "stun:stun.example.com:3478" {
		t.Fatalf("p2p ice servers: %v", got)
	}
}
