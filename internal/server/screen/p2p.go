//go:build screen

package screen

import (
	"context"
	"fmt"
	"sync"

	"github.com/pion/sdp/v3"

	pb "github.com/Floodww/RoutineOps/proto"
)

// P2PSession — режим p2p_preferred (§2): сервер только rendezvous, как hbbs у RustDesk.
// Оферта агента уходит браузеру, ответ браузера — агенту, медиа идут напрямую по ICE, а при
// неудаче прямого пути — через TURN из RTCConfig.P2PICEServers.
//
// Ограничения режима по построению:
//   - один зритель: у агента одна PeerConnection, и её второй конец — один браузер;
//   - записи нет: сервер не видит медиа (поэтому policy.go не даёт P2P при обязательной записи);
//   - без trickle ICE: агент шлёт оферту с уже собранными кандидатами, браузеру хватает
//     одного запроса. Кандидат отдельным сообщением — нарушение протокола, а не тишина.
type P2PSession struct {
	id          string
	sendToAgent func(*pb.SessionSignal) error
	onClose     func(string)

	mu        sync.Mutex
	offer     string
	offerSet  chan struct{}
	viewer    string
	answered  bool
	closed    bool
	closeOnce sync.Once
	done      chan struct{}
}

// NewP2PSession создаёт брокер сигнализации.
func NewP2PSession(id string, sendToAgent func(*pb.SessionSignal) error, onClose func(reason string)) (*P2PSession, error) {
	if sendToAgent == nil {
		return nil, fmt.Errorf("screen: SendToAgent required")
	}
	return &P2PSession{
		id: id, sendToAgent: sendToAgent, onClose: onClose,
		offerSet: make(chan struct{}), done: make(chan struct{}),
	}, nil
}

// HandleAgentSignal — SessionUp.signal от агента.
func (s *P2PSession) HandleAgentSignal(sig *pb.SessionSignal) error {
	m, ok := sig.GetMsg().(*pb.SessionSignal_SdpOffer)
	if !ok {
		return fmt.Errorf("%w: p2p accepts only a complete sdp_offer, got %T", errProtocol, sig.GetMsg())
	}
	if err := checkSDP(m.SdpOffer); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSessionClosed
	}
	if s.offer != "" {
		return fmt.Errorf("%w: renegotiation not supported", errProtocol)
	}
	s.offer = m.SdpOffer
	close(s.offerSet)
	return nil
}

// AgentOffer отдаёт браузеру оферту агента (ждёт её, если агент ещё не прислал) и закрепляет
// сеанс за этим зрителем. Права проверены вызывающим, как и в AddViewer.
func (s *P2PSession) AgentOffer(ctx context.Context, viewerID string) (string, error) {
	select {
	case <-s.offerSet:
	case <-s.done:
		return "", errSessionClosed
	case <-ctx.Done():
		return "", ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errSessionClosed
	}
	if s.viewer != "" && s.viewer != viewerID {
		return "", ErrTooManyViewers
	}
	s.viewer = viewerID
	return s.offer, nil
}

// ViewerAnswer передаёт ответ браузера агенту. Принимается один раз и только от зрителя,
// получившего оферту.
func (s *P2PSession) ViewerAnswer(viewerID, answerSDP string) error {
	if err := checkSDP(answerSDP); err != nil {
		return err
	}
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return errSessionClosed
	case s.viewer == "" || s.viewer != viewerID:
		s.mu.Unlock()
		return fmt.Errorf("%w: answer from a viewer that did not take the offer", errProtocol)
	case s.answered:
		s.mu.Unlock()
		return fmt.Errorf("%w: answer already sent", errProtocol)
	}
	s.answered = true
	s.mu.Unlock()
	return s.sendToAgent(&pb.SessionSignal{Msg: &pb.SessionSignal_SdpAnswer{SdpAnswer: answerSDP}})
}

// Close закрывает брокер. Медиа P2P живут между агентом и браузером, поэтому реальный конец
// сеанса — SessionControl.stop агенту, который шлёт владелец стрима.
func (s *P2PSession) Close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		if s.onClose != nil {
			s.onClose(reason)
		}
	})
}

// Done закрыт после Close.
func (s *P2PSession) Done() <-chan struct{} { return s.done }

// checkSDP — SDP хотя бы разбирается: мусор не должен доезжать до второй стороны.
func checkSDP(s string) error {
	var d sdp.SessionDescription
	if err := d.UnmarshalString(s); err != nil {
		return fmt.Errorf("%w: bad sdp: %v", errProtocol, err)
	}
	return nil
}
