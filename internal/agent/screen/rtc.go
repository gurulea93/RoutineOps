//go:build screen

package screen

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Видеоконвейер Этапа B (docs/REMOTE_DESKTOP_B_DESIGN.md §6): захват → I420 → кодер →
// VP8-дорожка PeerConnection. Сигнализация — по стриму сеанса (SessionUp/SessionDown.signal),
// который открывает менеджер сеанса Этапа A ПОСЛЕ SessionAccepted: конвейер не знает ни о
// сеансе, ни об аудите и не может начать раньше, чем его запустили.
//
// Агенту всё равно, relay или P2P: он шлёт оферту «на ту сторону сигнализации». Кто её
// примет — SFU сервера или браузер — решает сервер; отличие для агента одно — ice_servers.

// Профиль low (Ф1): потолки, а не цели. Геометрия ужимается с сохранением пропорций.
const (
	ProfileLowMaxWidth  = 1280
	ProfileLowMaxHeight = 800
	ProfileLowFPS       = 15
	ProfileLowKbps      = 800
	minAdaptiveKbps     = 150
)

// StreamerConfig — параметры конвейера одного сеанса.
type StreamerConfig struct {
	Codec       string // ScreenSessionCommand.video_codec; "" → vp8
	FPS         int    // 0 → профиль low
	BitrateKbps int    // потолок; REMB может только опустить ниже. 0 → профиль low
	MaxWidth    int    // 0 → профиль low
	MaxHeight   int
	ICEServers  []*pb.IceServer // ScreenSessionCommand.ice_servers

	// SendSignal кладёт сигнал в SessionUp.signal. Безопасен для конкурентного вызова —
	// обязанность владельца стрима.
	SendSignal func(*pb.SessionSignal) error

	// Тестовые швы: кодер без libvpx и сеть только loopback.
	NewEncoder    func(EncoderConfig) (Encoder, error)
	SettingEngine *webrtc.SettingEngine
}

// StreamStats — телеметрия для SessionStats.
type StreamStats struct {
	Frames  uint32
	Bytes   uint64
	Dropped uint32 // rate control кодера пропустил кадр
}

// Streamer — один сеанс WebRTC на агенте. Владеет захватчиком: закрывает его в Close.
type Streamer struct {
	cfg   StreamerConfig
	cap   Capturer
	enc   Encoder
	frame *I420
	srcW  int
	srcH  int

	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample

	wantKey  atomic.Bool
	bitrate  atomic.Int64
	answered atomic.Bool

	frames, dropped atomic.Uint32
	bytes           atomic.Uint64

	startOnce sync.Once
	closeOnce sync.Once
	loopWG    sync.WaitGroup // петля владеет кодером и захватчиком, пока работает
	stop      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	closing   bool
	endErr    error
}

// NewStreamer готовит конвейер: геометрию, кодер, PeerConnection. Сеть не трогает до Start.
// Ошибки — *ReasonError.
func NewStreamer(c Capturer, cfg StreamerConfig) (_ *Streamer, err error) {
	if cfg.SendSignal == nil {
		return nil, errors.New("screen: SendSignal required")
	}
	if cfg.Codec == "" {
		cfg.Codec = "vp8"
	}
	if cfg.Codec != "vp8" {
		return nil, reasonErr(ReasonCodecUnavailable, fmt.Errorf("codec %q", cfg.Codec))
	}
	if cfg.FPS <= 0 {
		cfg.FPS = ProfileLowFPS
	}
	if cfg.BitrateKbps <= 0 {
		cfg.BitrateKbps = ProfileLowKbps
	}
	if cfg.MaxWidth <= 0 || cfg.MaxHeight <= 0 {
		cfg.MaxWidth, cfg.MaxHeight = ProfileLowMaxWidth, ProfileLowMaxHeight
	}
	if cfg.NewEncoder == nil {
		codec := cfg.Codec
		cfg.NewEncoder = func(ec EncoderConfig) (Encoder, error) { return NewVideoEncoder(codec, ec) }
	}

	s := &Streamer{cfg: cfg, cap: c, stop: make(chan struct{}), done: make(chan struct{})}
	defer func() {
		if err != nil {
			s.release()
		}
	}()

	s.srcW, s.srcH = c.Size()
	w, h := FitGeometry(s.srcW, s.srcH, cfg.MaxWidth, cfg.MaxHeight)
	if w == 0 || h == 0 {
		return nil, reasonErr(ReasonCaptureUnsupported, fmt.Errorf("screen geometry %dx%d", s.srcW, s.srcH))
	}
	s.frame = NewI420(w, h)
	if s.enc, err = cfg.NewEncoder(EncoderConfig{Width: w, Height: h, FPS: cfg.FPS, BitrateKbps: cfg.BitrateKbps}); err != nil {
		if errors.Is(err, ErrCodecUnavailable) {
			return nil, reasonErr(ReasonCodecUnavailable, err)
		}
		return nil, reasonErr(ReasonEncoderFailed, err)
	}
	s.bitrate.Store(int64(cfg.BitrateKbps))
	s.wantKey.Store(true)

	if s.pc, err = newAgentPeerConnection(cfg); err != nil {
		return nil, reasonErr(ReasonTransportLost, err)
	}
	if s.track, err = webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "screen"); err != nil {
		return nil, reasonErr(ReasonTransportLost, err)
	}
	tr, err := s.pc.AddTransceiverFromTrack(s.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return nil, reasonErr(ReasonTransportLost, err)
	}
	go s.readRTCP(tr.Sender())
	s.pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			s.startOnce.Do(s.startLoop)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.end(reasonErr(ReasonTransportLost, fmt.Errorf("peer connection %s", st)))
		}
	})
	return s, nil
}

// FitGeometry вписывает w×h в потолок с сохранением пропорций; результат чётный (хрома 4:2:0
// без краевого полублока) и никогда не больше исходника.
func FitGeometry(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w > maxW || h > maxH {
		if w*maxH > h*maxW {
			w, h = maxW, h*maxW/w
		} else {
			w, h = w*maxH/h, maxH
		}
	}
	w, h = w&^1, h&^1
	if w == 0 || h == 0 {
		return 0, 0
	}
	return w, h
}

// Geometry — размер кадра после даунскейла (SessionHello.width/height; в эту же систему
// координат приходит ввод оператора).
func (s *Streamer) Geometry() (int, int) { return s.frame.Width, s.frame.Height }

func newAgentPeerConnection(cfg StreamerConfig) (*webrtc.PeerConnection, error) {
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: []webrtc.RTCPFeedback{
			{Type: webrtc.TypeRTCPFBGoogREMB},
			{Type: webrtc.TypeRTCPFBCCM, Parameter: "fir"},
			{Type: webrtc.TypeRTCPFBNACK},
			{Type: webrtc.TypeRTCPFBNACK, Parameter: "pli"},
		}},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, err
	}
	opts := []func(*webrtc.API){webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir)}
	if cfg.SettingEngine != nil {
		opts = append(opts, webrtc.WithSettingEngine(*cfg.SettingEngine))
	}
	return webrtc.NewAPI(opts...).NewPeerConnection(webrtc.Configuration{ICEServers: iceServers(cfg.ICEServers)})
}

func iceServers(in []*pb.IceServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(in))
	for _, s := range in {
		if len(s.GetUrls()) == 0 {
			continue
		}
		out = append(out, webrtc.ICEServer{URLs: s.GetUrls(), Username: s.GetUsername(), Credential: s.GetCredential()})
	}
	return out
}

// gatherTimeout — оферта уходит с уже собранными кандидатами: в P2P trickle не принимается
// (сервер отдаёт браузеру оферту одним ответом), а relay принимает и так. Один путь на оба
// режима. STUN за NAT может отвечать долго — отсюда запас.
const gatherTimeout = 10 * time.Second

// Start собирает кандидаты и шлёт оферту. Кадры пойдут, когда соединение установится.
func (s *Streamer) Start(ctx context.Context) error {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return s.end(reasonErr(ReasonTransportLost, err))
	}
	gathered := webrtc.GatheringCompletePromise(s.pc)
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return s.end(reasonErr(ReasonTransportLost, err))
	}
	ctx, cancel := context.WithTimeout(ctx, gatherTimeout)
	defer cancel()
	select {
	case <-gathered:
	case <-ctx.Done():
		return s.end(reasonErr(ReasonTransportLost, fmt.Errorf("ice gathering: %w", ctx.Err())))
	case <-s.done:
		return s.Err()
	}
	sig := &pb.SessionSignal{Msg: &pb.SessionSignal_SdpOffer{SdpOffer: s.pc.LocalDescription().SDP}}
	if err := s.cfg.SendSignal(sig); err != nil {
		return s.end(reasonErr(ReasonTransportLost, err))
	}
	return nil
}

// HandleSignal — SessionDown.signal от сервера.
func (s *Streamer) HandleSignal(sig *pb.SessionSignal) error {
	switch m := sig.GetMsg().(type) {
	case *pb.SessionSignal_SdpAnswer:
		if !s.answered.CompareAndSwap(false, true) {
			return s.end(reasonErr(ReasonSignalFailed, errors.New("second sdp answer")))
		}
		if err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SdpAnswer}); err != nil {
			return s.end(reasonErr(ReasonSignalFailed, err))
		}
		return nil
	case *pb.SessionSignal_KeyframeRequest:
		s.wantKey.Store(true)
		return nil
	case *pb.SessionSignal_Ice:
		if !s.answered.Load() {
			return s.end(reasonErr(ReasonSignalFailed, errors.New("ice before answer")))
		}
		return s.pc.AddICECandidate(webrtc.ICECandidateInit{
			Candidate: m.Ice.GetCandidate(), SDPMid: ptr(m.Ice.GetSdpMid()), SDPMLineIndex: ptr(uint16(m.Ice.GetSdpMlineIndex())),
		})
	default:
		return s.end(reasonErr(ReasonSignalFailed, fmt.Errorf("unexpected signal %T", m)))
	}
}

func ptr[T any](v T) *T { return &v }

// readRTCP: PLI/FIR → ключевой кадр; REMB → битрейт кодера, но не выше потолка профиля.
func (s *Streamer) readRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p := p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.wantKey.Store(true)
			case *rtcp.ReceiverEstimatedMaximumBitrate:
				kbps := int64(p.Bitrate / 1000)
				kbps = max(min(kbps, int64(s.cfg.BitrateKbps)), minAdaptiveKbps)
				s.bitrate.Store(kbps)
			}
		}
	}
}

func (s *Streamer) startLoop() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.loopWG.Add(1) // под mu: end() не может начать Wait раньше этого Add
	s.mu.Unlock()
	go func() {
		err := s.loop()
		s.loopWG.Done() // до end(): иначе end ждал бы сам себя
		if err != nil {
			s.end(err)
		}
	}()
}

// loop — петля кадров. «Последний кадр выигрывает»: тикер не копит пропущенные тики, и
// медленный кодер снижает частоту, а не наращивает задержку. nil — остановлена извне.
func (s *Streamer) loop() error {
	tick := time.NewTicker(time.Second / time.Duration(s.cfg.FPS))
	defer tick.Stop()
	appliedKbps := int64(s.cfg.BitrateKbps)
	last := time.Now()
	for {
		select {
		case <-s.stop:
			return nil
		case <-tick.C:
		}
		if kbps := s.bitrate.Load(); kbps != appliedKbps {
			if err := s.enc.SetBitrate(int(kbps)); err == nil {
				appliedKbps = kbps
			}
		}
		pix, stride, err := s.cap.Capture()
		if err != nil {
			return asReason(err, ReasonCaptureFailed)
		}
		if err := s.frame.FromBGRAScaled(pix, stride, s.srcW, s.srcH); err != nil {
			return reasonErr(ReasonCaptureFailed, err)
		}
		key := s.wantKey.Swap(false)
		out, err := s.enc.Encode(s.frame, key)
		if err != nil {
			return reasonErr(ReasonEncoderFailed, err)
		}
		if out == nil {
			s.dropped.Add(1)
			if key {
				s.wantKey.Store(true) // пропущенный ключевой кадр всё ещё нужен
			}
			continue
		}
		now := time.Now()
		if err := s.track.WriteSample(media.Sample{Data: out, Duration: now.Sub(last)}); err != nil {
			return reasonErr(ReasonTransportLost, err)
		}
		last = now
		s.frames.Add(1)
		s.bytes.Add(uint64(len(out)))
	}
}

func asReason(err error, fallback EndReason) error {
	var re *ReasonError
	if errors.As(err, &re) {
		return err
	}
	return reasonErr(fallback, err)
}

// Stats — счётчики для SessionStats.
func (s *Streamer) Stats() StreamStats {
	return StreamStats{Frames: s.frames.Load(), Bytes: s.bytes.Load(), Dropped: s.dropped.Load()}
}

// Close останавливает конвейер (стоп оператора, конец стрима).
func (s *Streamer) Close() { s.end(reasonErr(ReasonStopped, nil)) }

// Done закрыт, когда конвейер остановлен; Err — почему.
func (s *Streamer) Done() <-chan struct{} { return s.done }

// Err — причина остановки (*ReasonError) или nil, пока работает.
func (s *Streamer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endErr
}

// end — единственная точка остановки: первая причина побеждает, остальные отбрасываются.
// Кодер и захватчик закрываются только после выхода петли: Encode в закрытый кодер libvpx —
// обращение к освобождённой C-памяти, а не ошибка.
func (s *Streamer) end(err error) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing, s.endErr = true, err
		s.mu.Unlock()
		close(s.stop)
		s.loopWG.Wait()
		s.release()
		close(s.done)
	})
	return s.Err()
}

func (s *Streamer) release() {
	if s.pc != nil {
		_ = s.pc.Close()
	}
	if s.enc != nil {
		s.enc.Close()
	}
	if s.cap != nil {
		_ = s.cap.Close()
	}
}
