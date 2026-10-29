//go:build screen

package screen

import (
	"errors"
	"fmt"
	"net"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	pb "github.com/Floodww/RoutineOps/proto"
)

// RTCConfig — сетевая сторона WebRTC на сервере (Этап B, §5).
//
// Один UDP-порт на все сеансы (ICE UDP mux), а не диапазон: в firewall и docker-compose
// открывается ровно один порт, как у hbbr. ICE-TCP — запасной путь для сетей, где UDP закрыт;
// без него такие сети падают на RSF1.
type RTCConfig struct {
	UDPAddr string // ":50052"; "127.0.0.1:0" — эфемерный порт (тесты)
	TCPAddr string // "" — ICE-TCP выключен
	// PublicIPs — адреса, которые сервер объявляет в кандидатах вместо локальных (сервер за
	// NAT/в docker). Только IP: имя хоста в SDP браузер не примет.
	PublicIPs []string
	// IncludeLoopback — объявлять 127.0.0.1 (тесты и стенд на одной машине).
	IncludeLoopback bool
	// P2PICEServers — STUN/TURN для режима p2p_preferred. Отдаются агенту и браузеру; сам
	// сервер в P2P медиа не касается. TURN здесь и есть «relay как запасной путь» P2P.
	P2PICEServers []webrtc.ICEServer
}

// RTC — общая для всех сеансов фабрика PeerConnection.
type RTC struct {
	api    *webrtc.API
	cfg    RTCConfig
	udp    net.PacketConn
	tcp    net.Listener
	udpMux ice.UDPMux
	tcpMux ice.TCPMux
}

// NewRTC открывает сокеты и собирает API. Закрывать — Close.
func NewRTC(cfg RTCConfig) (_ *RTC, err error) {
	for _, ip := range cfg.PublicIPs {
		if net.ParseIP(ip) == nil {
			return nil, fmt.Errorf("screen: public ip %q is not an IP address", ip)
		}
	}
	if cfg.UDPAddr == "" {
		return nil, errors.New("screen: rtc udp address required")
	}

	r := &RTC{cfg: cfg}
	defer func() {
		if err != nil {
			r.Close()
		}
	}()

	if r.udp, err = net.ListenPacket("udp", cfg.UDPAddr); err != nil {
		return nil, fmt.Errorf("screen: rtc udp listen: %w", err)
	}
	se := webrtc.SettingEngine{}
	r.udpMux = webrtc.NewICEUDPMux(nil, r.udp)
	se.SetICEUDPMux(r.udpMux)
	networks := []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6}

	if cfg.TCPAddr != "" {
		if r.tcp, err = net.Listen("tcp", cfg.TCPAddr); err != nil {
			return nil, fmt.Errorf("screen: rtc tcp listen: %w", err)
		}
		r.tcpMux = webrtc.NewICETCPMux(nil, r.tcp, 8)
		se.SetICETCPMux(r.tcpMux)
		networks = append(networks, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6)
	}
	se.SetNetworkTypes(networks)
	se.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	// mDNS-имена вместо IP прячут адрес от браузера в LAN — серверу прятать нечего, а
	// разрешать .local браузеру потом нечем.
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	if len(cfg.PublicIPs) > 0 {
		if err = se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        cfg.PublicIPs,
			AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode:            webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return nil, fmt.Errorf("screen: rtc public ips: %w", err)
		}
	}

	me, ir, err := newMediaEngine()
	if err != nil {
		return nil, err
	}
	r.api = webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir))
	return r, nil
}

// newMediaEngine — только VP8 (Этап B, §2). Оферта с одним H.264 получит отказ по m-строке, и
// сеанс умрёт кодом, а не пустым экраном. Обратная связь — NACK, PLI, FIR, REMB: PLI/FIR
// зрителей превращаются в keyframe_request агенту.
func newMediaEngine() (*webrtc.MediaEngine, *interceptor.Registry, error) {
	me := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{
		{Type: webrtc.TypeRTCPFBGoogREMB},
		{Type: webrtc.TypeRTCPFBCCM, Parameter: "fir"},
		{Type: webrtc.TypeRTCPFBNACK},
		{Type: webrtc.TypeRTCPFBNACK, Parameter: "pli"},
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: fb},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, nil, fmt.Errorf("screen: register vp8: %w", err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, nil, fmt.Errorf("screen: interceptors: %w", err)
	}
	return me, ir, nil
}

// UDPAddr — фактический адрес UDP-сокета (эфемерный порт в тестах).
func (r *RTC) UDPAddr() net.Addr { return r.udp.LocalAddr() }

// P2PICEServers — что отдать агенту и браузеру в режиме P2P.
func (r *RTC) P2PICEServers() []webrtc.ICEServer { return r.cfg.P2PICEServers }

// ICEServersFor — ScreenSessionCommand.ice_servers для выбранного режима. В relay пусто:
// агент соединяется с сервером, который достижим напрямую, и STUN/TURN ему ни к чему.
func (r *RTC) ICEServersFor(m Mode) []*pb.IceServer {
	if m != ModeP2P {
		return nil
	}
	out := make([]*pb.IceServer, 0, len(r.cfg.P2PICEServers))
	for _, s := range r.cfg.P2PICEServers {
		cred, _ := s.Credential.(string)
		out = append(out, &pb.IceServer{Urls: s.URLs, Username: s.Username, Credential: cred})
	}
	return out
}

func (r *RTC) newPeerConnection() (*webrtc.PeerConnection, error) {
	return r.api.NewPeerConnection(webrtc.Configuration{})
}

// Close закрывает сокеты. Сеансы закрываются раньше — каждый своим Close.
func (r *RTC) Close() error {
	var errs []error
	if r.udpMux != nil {
		errs = append(errs, r.udpMux.Close())
	} else if r.udp != nil {
		errs = append(errs, r.udp.Close())
	}
	if r.tcpMux != nil {
		errs = append(errs, r.tcpMux.Close())
	} else if r.tcp != nil {
		errs = append(errs, r.tcp.Close())
	}
	return errors.Join(errs...)
}
