//go:build screen

package screen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Session — медиа-сторона одного WebRTC-сеанса: relay (SFU-lite) или P2P-брокер. Стрим
// сеанса (service.go) пока ведёт только RSF1; при подключении WebRTC он будет держать её
// рядом со строкой screen_sessions: SessionUp.signal → HandleAgentSignal,
// SendToAgent → SessionDown.signal.
type Session interface {
	HandleAgentSignal(sig *pb.SessionSignal) error
	Close(reason string)
	Done() <-chan struct{}
}

var (
	errSessionClosed  = errors.New("screen: session closed")
	ErrTooManyViewers = errors.New("screen: viewer limit reached")
	errProtocol       = errors.New("screen: signal protocol violation")
)

// SessionOptions — параметры сеанса relay.
type SessionOptions struct {
	// SendToAgent кладёт сигнал в SessionDown.signal. Обязан быть безопасен для
	// конкурентного вызова: gRPC Send — нет, и сериализует его владелец стрима.
	SendToAgent func(*pb.SessionSignal) error
	Recorder    *Recorder // nil — без записи
	MaxViewers  int       // 0 → 4
	// KeyframeMinInterval — не чаще одного keyframe_request: PLI от нескольких зрителей с
	// потерями иначе превращает поток в одни ключевые кадры. 0 → 500 мс.
	KeyframeMinInterval time.Duration
	// OnClose — сеанс закрылся (причина — строкой, как причины завершения в proto).
	OnClose func(reason string)
	// Log — журнал состояний соединений (агент/зрители). nil — slog.Default.
	Log *slog.Logger
}

// gatherTimeout — сервер отдаёт SDP с уже собранными кандидатами (не trickle): кандидаты
// сервера — только host с mux-сокетов, сбор мгновенный, а браузеру хватает одного POST.
const gatherTimeout = 5 * time.Second

// RelaySession — SFU-lite: одна входящая дорожка агента, N зрителей, запись на сервере.
//
// Одна TrackLocalStaticRTP на всех зрителей: pion сам переписывает SSRC/PT под каждую
// привязку, а пакет агента расходится всем без копий на уровне сеанса.
type RelaySession struct {
	id    string
	rtc   *RTC
	opts  SessionOptions
	local *webrtc.TrackLocalStaticRTP

	mu        sync.Mutex
	agent     *webrtc.PeerConnection
	agentSSRC webrtc.SSRC
	haveTrack bool
	viewers   map[string]*webrtc.PeerConnection
	lastKF    time.Time
	closed    bool
	// fwd — сколько RTP-пакетов агента разослано зрителям: по нему в логе видно, дошло ли
	// видео до сервера, когда у оператора чёрный экран.
	fwd       atomic.Int64
	closeOnce sync.Once
	done      chan struct{}
	endReason string
}

// NewRelaySession создаёт сеанс relay. Агентский PeerConnection появится по его оферте.
func (r *RTC) NewRelaySession(id string, opts SessionOptions) (*RelaySession, error) {
	if opts.SendToAgent == nil {
		return nil, errors.New("screen: SendToAgent required")
	}
	if opts.MaxViewers <= 0 {
		opts.MaxViewers = 4
	}
	if opts.KeyframeMinInterval <= 0 {
		opts.KeyframeMinInterval = 500 * time.Millisecond
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	opts.Log = opts.Log.With("session_id", id)
	local, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "screen-"+id)
	if err != nil {
		return nil, fmt.Errorf("screen: local track: %w", err)
	}
	return &RelaySession{
		id: id, rtc: r, opts: opts, local: local,
		viewers: map[string]*webrtc.PeerConnection{},
		done:    make(chan struct{}),
	}, nil
}

// HandleAgentSignal — SessionUp.signal от агента.
func (s *RelaySession) HandleAgentSignal(sig *pb.SessionSignal) error {
	switch m := sig.GetMsg().(type) {
	case *pb.SessionSignal_SdpOffer:
		return s.agentOffer(m.SdpOffer)
	case *pb.SessionSignal_Ice:
		s.mu.Lock()
		pc := s.agent
		s.mu.Unlock()
		if pc == nil {
			return fmt.Errorf("%w: ice before offer", errProtocol)
		}
		return pc.AddICECandidate(iceInit(m.Ice))
	default:
		return fmt.Errorf("%w: agent sent %T", errProtocol, m)
	}
}

func (s *RelaySession) agentOffer(sdp string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	if s.agent != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: renegotiation not supported", errProtocol)
	}
	pc, err := s.rtc.newPeerConnection()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("screen: agent pc: %w", err)
	}
	s.agent = pc
	s.mu.Unlock()

	pc.OnTrack(s.onAgentTrack)
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.opts.Log.Info("screen rtc: agent connection", "state", st.String())
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			s.Close("AGENT_GONE")
		}
	})

	answer, err := answerOffer(pc, sdp)
	if err != nil {
		s.Close("SIGNAL_FAILED")
		return err
	}
	if !answerAcceptsVP8(answer) {
		s.Close("CODEC_UNSUPPORTED")
		return fmt.Errorf("%w: agent offer has no acceptable vp8 video", errProtocol)
	}
	return s.opts.SendToAgent(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpAnswer{SdpAnswer: answer}})
}

func (s *RelaySession) onAgentTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeVP8) || track.Kind() != webrtc.RTPCodecTypeVideo {
		s.Close("CODEC_UNSUPPORTED")
		return
	}
	s.opts.Log.Info("screen rtc: agent track", "codec", track.Codec().MimeType, "ssrc", uint32(track.SSRC()))
	s.mu.Lock()
	s.agentSSRC, s.haveTrack = track.SSRC(), true
	viewers := len(s.viewers)
	s.mu.Unlock()
	if viewers > 0 {
		s.requestKeyframe()
	}

	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return // конец дорожки — сеанс закроет OnConnectionStateChange
		}
		if s.opts.Recorder != nil {
			// Копия: samplebuilder держит пакет, а WriteRTP ниже переписывает заголовок.
			_ = s.opts.Recorder.WriteRTP(pkt.Clone())
		}
		if err := s.local.WriteRTP(pkt); err == nil && s.fwd.Add(1) == 1 {
			s.opts.Log.Info("screen rtc: first video packet from agent")
		}
	}
}

// AddViewer принимает оферту браузера (POST /screen-sessions/{id}/offer) и возвращает ответ.
// Права зрителя проверяет вызывающий (RBAC, активный сеанс) ДО вызова: сюда приходит уже
// авторизованный viewerID. Повторная оферта того же зрителя (перезагрузка вкладки) заменяет
// его прежнее соединение.
func (s *RelaySession) AddViewer(ctx context.Context, viewerID, offerSDP string) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", errSessionClosed
	}
	old := s.viewers[viewerID]
	if old == nil && len(s.viewers) >= s.opts.MaxViewers {
		s.mu.Unlock()
		return "", ErrTooManyViewers
	}
	s.mu.Unlock()

	pc, err := s.rtc.newPeerConnection()
	if err != nil {
		return "", fmt.Errorf("screen: viewer pc: %w", err)
	}
	sender, err := pc.AddTrack(s.local)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("screen: viewer track: %w", err)
	}
	go s.readViewerRTCP(sender)
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.opts.Log.Info("screen rtc: viewer connection", "viewer", viewerID, "state", st.String())
		switch st {
		case webrtc.PeerConnectionStateConnected:
			s.requestKeyframe() // новый зритель не может начать с межкадра
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.removeViewer(viewerID, pc)
		}
	})

	answer, err := answerOfferCtx(ctx, pc, offerSDP)
	if err != nil {
		_ = pc.Close()
		return "", err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = pc.Close()
		return "", errSessionClosed
	}
	old = s.viewers[viewerID]
	s.viewers[viewerID] = pc
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return answer, nil
}

// RemoveViewer — зритель закрыл панель.
func (s *RelaySession) RemoveViewer(viewerID string) {
	s.mu.Lock()
	pc := s.viewers[viewerID]
	delete(s.viewers, viewerID)
	s.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
}

func (s *RelaySession) removeViewer(viewerID string, pc *webrtc.PeerConnection) {
	s.mu.Lock()
	if s.viewers[viewerID] == pc {
		delete(s.viewers, viewerID)
	}
	s.mu.Unlock()
}

// Viewers — сколько зрителей подключено.
func (s *RelaySession) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.viewers)
}

// readViewerRTCP: PLI/FIR зрителя → keyframe_request агенту. Чтение RTCP обязательно и без
// этого: иначе interceptors (NACK) не получают обратную связь.
func (s *RelaySession) readViewerRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.requestKeyframe()
			}
		}
	}
}

func (s *RelaySession) requestKeyframe() {
	s.mu.Lock()
	if s.closed || s.agent == nil || time.Since(s.lastKF) < s.opts.KeyframeMinInterval {
		s.mu.Unlock()
		return
	}
	s.lastKF = time.Now()
	pc, ssrc, have := s.agent, s.agentSSRC, s.haveTrack
	s.mu.Unlock()

	_ = s.opts.SendToAgent(&pb.SessionSignal{Msg: &pb.SessionSignal_KeyframeRequest{KeyframeRequest: true}})
	if have {
		// Плюс стандартный PLI по RTCP: кодер на стороне агента может слушать любой из двух.
		_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
	}
}

// Close закрывает сеанс: зрителей, агента, запись. Повторный вызов безопасен.
func (s *RelaySession) Close(reason string) {
	s.closeOnce.Do(func() {
		s.opts.Log.Info("screen rtc: session closed", "reason", reason, "packets_forwarded", s.fwd.Load())
		s.mu.Lock()
		s.closed, s.endReason = true, reason
		viewers := s.viewers
		s.viewers = map[string]*webrtc.PeerConnection{}
		agent := s.agent
		s.mu.Unlock()

		for _, pc := range viewers {
			_ = pc.Close()
		}
		if agent != nil {
			_ = agent.Close()
		}
		if s.opts.Recorder != nil {
			_ = s.opts.Recorder.Close()
		}
		close(s.done)
		if s.opts.OnClose != nil {
			s.opts.OnClose(reason)
		}
	})
}

// Done закрыт после Close.
func (s *RelaySession) Done() <-chan struct{} { return s.done }

// EndReason — причина закрытия ("" пока сеанс жив).
func (s *RelaySession) EndReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endReason
}

func answerOffer(pc *webrtc.PeerConnection, sdp string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gatherTimeout)
	defer cancel()
	return answerOfferCtx(ctx, pc, sdp)
}

func answerOfferCtx(ctx context.Context, pc *webrtc.PeerConnection, sdp string) (string, error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", fmt.Errorf("%w: bad offer: %v", errProtocol, err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("screen: create answer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("screen: set local: %w", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gatherTimeout)
		defer cancel()
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return "", fmt.Errorf("screen: ice gathering: %w", ctx.Err())
	}
	return pc.LocalDescription().SDP, nil
}

// answerAcceptsVP8 — в нашем ответе есть принятая (порт != 0) видео-m-строка с VP8 на приём.
// Смотрим в SDP, а не в трансиверы: pion принимает оферту без общего кодека без ошибки, и
// трансивер выглядит живым, хотя кадров по нему не придёт никогда.
func answerAcceptsVP8(answer string) bool {
	var d sdp.SessionDescription
	if d.UnmarshalString(answer) != nil {
		return false
	}
	for _, m := range d.MediaDescriptions {
		if m.MediaName.Media != "video" || m.MediaName.Port.Value == 0 {
			continue
		}
		recv := true
		for _, a := range m.Attributes {
			if a.Key == "inactive" || a.Key == "sendonly" {
				recv = false
			}
		}
		if !recv {
			continue
		}
		for _, a := range m.Attributes {
			if a.Key == "rtpmap" && strings.Contains(strings.ToUpper(a.Value), "VP8/90000") {
				return true
			}
		}
	}
	return false
}

func iceInit(c *pb.IceCandidate) webrtc.ICECandidateInit {
	mid := c.GetSdpMid()
	idx := uint16(c.GetSdpMlineIndex())
	return webrtc.ICECandidateInit{Candidate: c.GetCandidate(), SDPMid: &mid, SDPMLineIndex: &idx}
}
