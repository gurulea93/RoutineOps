//go:build screen

package screen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	pb "github.com/Floodww/RoutineOps/proto"
)

// sendSignal — SendToAgent релея: кладёт сигнал в очередь стрима (писать в стрим может
// только его петля). Полная очередь — ошибка: сигналы нельзя терять молча.
func (b *hub) sendSignal(sig *pb.SessionSignal) error {
	select {
	case b.sig <- sig:
		return nil
	default:
		return errors.New("screen: signal queue full")
	}
}

func (b *hub) setRelay(r *RelaySession) {
	b.mu.Lock()
	b.relay = r
	b.mu.Unlock()
}

func (b *hub) getRelay() *RelaySession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.relay
}

// touch — зритель жив (замена опросу /live для WebRTC).
func (b *hub) touch() {
	b.mu.Lock()
	b.lastPoll = time.Now()
	b.mu.Unlock()
}

// httpOffer — POST /screen-sessions/{id}/offer {"sdp": "..."} → {"sdp": answer, "width", "height"}.
// Оферту принимает только владелец сеанса; чужой сеанс неотличим от несуществующего.
func (s *Service) httpOffer(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h := s.hubs.get(chi.URLParam(r, "id"))
	if h == nil {
		writeErr(w, http.StatusServiceUnavailable, "stream_not_open")
		return
	}
	h.mu.Lock()
	mine := h.tenantID == tenantID && h.operatorID == userID
	cw, ch := h.w, h.h
	h.mu.Unlock()
	if !mine {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	relay := h.getRelay()
	if relay == nil {
		writeErr(w, http.StatusConflict, "not_webrtc")
		return
	}
	var vid [8]byte
	_, _ = rand.Read(vid[:])
	answer, err := relay.AddViewer(r.Context(), userID+"-"+hex.EncodeToString(vid[:]), req.SDP)
	switch {
	case errors.Is(err, ErrTooManyViewers):
		writeErr(w, http.StatusConflict, "too_many_viewers")
		return
	case err != nil:
		s.log.Warn("screen: viewer offer failed", "err", err)
		writeErr(w, http.StatusBadGateway, "offer_failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sdp": answer, "width": cw, "height": ch})
}

// iceFor — ice_servers для приглашения; релей их не требует.
func (s *Service) iceFor(c Choice) []*pb.IceServer {
	if s.rtc == nil {
		return nil
	}
	return s.rtc.ICEServersFor(c.Mode)
}
