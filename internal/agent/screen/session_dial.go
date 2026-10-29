//go:build screen

package screen

import (
	"errors"
	"fmt"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
	pb "github.com/Floodww/RoutineOps/proto"
)

// acceptTimeout — сколько ждём SessionAccepted. Фиксированный потолок: сервер
// отвечает после коммита аудита, это доли секунды; дольше — сеанс не начинается, а
// не висит с поднятой плашкой и без зрителя.
const acceptTimeout = 15 * time.Second

// sessionStream — открытый стрим сеанса и его отдельное соединение.
type sessionStream struct {
	pb.AgentService_ScreenSessionClient
	close func() // отменяет стрим и закрывает соединение
}

// dialSession открывает стрим сеанса на ОТДЕЛЬНОМ соединении (DialStream, ADR-8 п.1:
// кадры не делят HTTP/2 с heartbeat), шлёт hello и ждёт SessionAccepted. До успешного
// возврата кадров не шлёт никто: другого пути к стриму, кроме этой функции, нет.
//
// session_id в hello — лишь ссылка на строку screen_sessions. Права даёт сертификат
// mTLS этого соединения: сервер сверяет устройство из него с device_id сеанса.
//
// Ошибка — сеанс закрыт здесь же (s.end) с явной причиной; она же возвращается.
func dialSession(d *transport.Dialer, s *session, h wireHello, agentVersion string) (*sessionStream, EndReason, error) {
	fail := func(r EndReason, err error) (*sessionStream, EndReason, error) {
		r, _ = s.end(r, err.Error())
		return nil, r, err
	}
	conn, err := d.DialStream()
	if err != nil {
		return fail(ReasonTransportLost, fmt.Errorf("dial: %w", err))
	}
	stream, err := pb.NewAgentServiceClient(conn).ScreenSession(s.ctx)
	if err != nil {
		_ = conn.Close()
		return fail(ReasonTransportLost, fmt.Errorf("open stream: %w", err))
	}
	ss := &sessionStream{AgentService_ScreenSessionClient: stream, close: func() { s.cancel(); _ = conn.Close() }}

	err = stream.Send(&pb.SessionUp{Msg: &pb.SessionUp_Hello{Hello: &pb.SessionHello{
		SessionId: s.id, Width: int32(h.Width), Height: int32(h.Height),
		Profile: h.Profile, ProfileFallback: h.ProfileFallback, AgentVersion: agentVersion,
	}}})
	if err != nil {
		ss.close()
		return fail(ReasonTransportLost, fmt.Errorf("send hello: %w", err))
	}

	got := make(chan error, 1)
	go func() {
		m, err := stream.Recv()
		if err == nil && m.GetAccepted() == nil {
			err = errors.New("first server message is not SessionAccepted")
		}
		got <- err
	}()
	select {
	case err = <-got:
	case <-time.After(acceptTimeout):
		err = fmt.Errorf("no SessionAccepted within %s", acceptTimeout)
	}
	if err != nil {
		ss.close()
		return fail(ReasonTransportLost, fmt.Errorf("session not accepted: %w", err))
	}
	if !s.activate() {
		ss.close()
		return fail(ReasonProtocolError, errors.New("session left 'requested' before accept"))
	}
	return ss, "", nil
}
