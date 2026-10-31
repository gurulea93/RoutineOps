//go:build screen

package screen

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Буфер обмена сеанса с управлением (сотрудник → оператор). Метка сигнала — как у агента
// (agent/screen.ClipboardMid); импортировать агентский пакет сервер не может.
const (
	clipboardMid      = "clipboard"
	maxClipboardBytes = 128 << 10
)

// httpClipboard — GET /screen-sessions/{id}/clipboard: последний текст, скопированный
// сотрудником. Только оператору этого сеанса и только пока сеанс идёт.
func (s *Service) httpClipboard(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	h := s.hubs.get(chi.URLParam(r, "id"))
	if h == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.mu.Lock()
	mine := h.tenantID == tenantID && h.operatorID == userID
	h.mu.Unlock()
	if !mine {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	seq, text := h.clipboard()
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "text": text})
}
