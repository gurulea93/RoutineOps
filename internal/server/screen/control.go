//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/Floodww/RoutineOps/internal/server/storage"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Управление мышью и клавиатурой (Ф3). Панель шлёт пачки (screeninput.ts), сервер
// проверяет их и передаёт агенту в SessionDown.input. Права на управление — свойство
// сеанса, заданное приглашением; здесь они только СВЕРЯЮТСЯ и могут лишь сниматься.

const (
	// inputQueue — сколько пачек ждут отправки агенту. Панель шлёт ~20 пачек/с; запас в
	// 64 — три секунды затора. Дальше 503: оператор увидит «ввод отстаёт», а не очередь,
	// которая выполнит старые клики через минуту.
	inputQueue = 64

	maxInputBody    = 64 << 10
	maxInputEvents  = 256
	maxEventText    = 256  // рун в одном событии text
	maxBatchText    = 2048 // рун во всей пачке
	maxWheelClicks  = 20
	maxFrameSide    = 16384
	maxKeyCodeBytes = 32
)

type inputEventReq struct {
	Kind   string `json:"kind"`
	X      int32  `json:"x"`
	Y      int32  `json:"y"`
	Button int32  `json:"button"`
	WheelX int32  `json:"wheel_x"`
	WheelY int32  `json:"wheel_y"`
	Code   string `json:"code"`
	Text   string `json:"text"`
}

type inputBatchReq struct {
	FrameWidth  int32           `json:"frame_width"`
	FrameHeight int32           `json:"frame_height"`
	ReleaseAll  bool            `json:"release_all"`
	Events      []inputEventReq `json:"events"`
}

// inputCounts — счётчики ввода между сбросами в screen_input_events. Строка на событие
// превратила бы журнал в нечитаемую простыню; пишем агрегаты. Что именно набрано —
// не пишем никогда (§9.21 п.6): только сколько.
type inputCounts struct{ clicks, keys, texts, scrolls, moves int }

func (c inputCounts) empty() bool {
	return c.clicks+c.keys+c.texts+c.scrolls+c.moves == 0
}

func (b *hub) setOwner(tenantID string, sess *SessionRow) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tenantID = tenantID
	if sess.OperatorID != nil {
		b.operatorID = *sess.OperatorID
	}
	b.control = sess.Control
}

// inputVerdict — исход попытки положить пачку в очередь.
type inputVerdict int

const (
	inputAccepted  inputVerdict = iota
	inputNotYours               // чужой сеанс
	inputNoControl              // управления нет или его вернули
	inputClosed                 // сеанс закрыт
	inputBacklog                // очередь к агенту полна
)

// sendInput проверяет права и кладёт пачку в очередь стрима. Без обращения к БД.
func (b *hub) sendInput(tenantID, userID string, in *pb.SessionInput, c inputCounts) inputVerdict {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.tenantID != tenantID || b.operatorID != userID:
		return inputNotYours
	case b.closed:
		return inputClosed
	case !b.control || b.returned:
		return inputNoControl
	}
	select {
	case b.input <- in:
		b.counts.clicks += c.clicks
		b.counts.keys += c.keys
		b.counts.texts += c.texts
		b.counts.scrolls += c.scrolls
		b.counts.moves += c.moves
		return inputAccepted
	default:
		return inputBacklog
	}
}

// giveBack снимает управление: ввод больше не принимается, очередь сбрасывается, а
// стриму приказано сузить область агенту. Истина — если управление действительно было.
func (b *hub) giveBack() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.control || b.returned {
		return false
	}
	b.returned = true
drain:
	for {
		select {
		case <-b.input:
		default:
			break drain
		}
	}
	select {
	case b.narrow <- struct{}{}:
	default:
	}
	return true
}

func (b *hub) takeCounts() inputCounts {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.counts
	b.counts = inputCounts{}
	return c
}

// buildInput проверяет пачку и собирает pb.SessionInput. Неизвестный вид события
// отбрасывается молча: панель новее сервера — не причина рвать ввод целиком.
func buildInput(req *inputBatchReq) (*pb.SessionInput, inputCounts, bool) {
	var c inputCounts
	if req.FrameWidth < 1 || req.FrameHeight < 1 || req.FrameWidth > maxFrameSide || req.FrameHeight > maxFrameSide ||
		len(req.Events) > maxInputEvents {
		return nil, c, false
	}
	out := &pb.SessionInput{
		FrameWidth: req.FrameWidth, FrameHeight: req.FrameHeight,
		ReleaseAll: req.ReleaseAll, SentAtMs: time.Now().UnixMilli(),
	}
	textRunes := 0
	for _, e := range req.Events {
		ev := &pb.InputEvent{Kind: e.Kind}
		switch e.Kind {
		case "mouse_move":
			ev.X, ev.Y = e.X, e.Y
			c.moves++
		case "mouse_down", "mouse_up":
			if e.Button < 0 || e.Button > 2 {
				continue
			}
			ev.X, ev.Y, ev.Button = e.X, e.Y, e.Button
			if e.Kind == "mouse_down" {
				c.clicks++
			}
		case "wheel":
			ev.WheelX = min(max(e.WheelX, -maxWheelClicks), maxWheelClicks)
			ev.WheelY = min(max(e.WheelY, -maxWheelClicks), maxWheelClicks)
			c.scrolls++
		case "key_down", "key_up":
			if e.Code == "" || len(e.Code) > maxKeyCodeBytes {
				continue
			}
			ev.Code = e.Code
			if e.Kind == "key_down" {
				c.keys++
			}
		case "text":
			n := utf8.RuneCountInString(e.Text)
			if n == 0 || n > maxEventText || !utf8.ValidString(e.Text) {
				continue
			}
			if textRunes += n; textRunes > maxBatchText {
				return nil, inputCounts{}, false
			}
			ev.Text = e.Text
			c.texts += n
		default:
			continue
		}
		out.Events = append(out.Events, ev)
	}
	return out, c, true
}

// httpInput — POST /screen-sessions/{id}/input.
//
// Коды ответа различают то, что панель обязана различать (screeninput.ts inputOutcome):
// 503 — временно (стрим ещё не открыт либо затор у агента), 409/410 — управления в этом
// сеансе больше не будет.
func (s *Service) httpInput(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	var req inputBatchReq
	if err := json.NewDecoder(io.LimitReader(r.Body, maxInputBody)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	in, counts, valid := buildInput(&req)
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad_input_batch")
		return
	}
	id := chi.URLParam(r, "id")

	h := s.hubs.get(id)
	if h == nil {
		// Хаба нет: сеанса нет, он уже закрыт и забыт, либо стрим ещё не открыт.
		s.inputWithoutHub(w, r, tenantID, userID, id)
		return
	}
	switch h.sendInput(tenantID, userID, in, counts) {
	case inputAccepted:
		w.WriteHeader(http.StatusNoContent)
	case inputNotYours:
		http.Error(w, "not found", http.StatusNotFound) // чужой сеанс неотличим от несуществующего
	case inputNoControl:
		writeErr(w, http.StatusConflict, "control_not_granted")
	case inputClosed:
		writeErr(w, http.StatusGone, "session_ended")
	case inputBacklog:
		writeErr(w, http.StatusServiceUnavailable, "input_backlog")
	}
}

func (s *Service) inputWithoutHub(w http.ResponseWriter, r *http.Request, tenantID, userID, id string) {
	var sess *SessionRow
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		sess, err = s.st.get(ctx, q, id)
		return err
	})
	switch {
	case errors.Is(err, ErrNotFound) || (err == nil && (sess.OperatorID == nil || *sess.OperatorID != userID)):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		s.internal(w, "input lookup", err)
	case sess.Status == "ended":
		writeErr(w, http.StatusGone, "session_ended")
	case !sess.Control || sess.ControlReturnedAt != nil:
		writeErr(w, http.StatusConflict, "control_not_granted")
	default:
		// Приглашение едет на устройство, захватчик поднимается — стрима ещё нет.
		writeErr(w, http.StatusServiceUnavailable, "stream_not_open")
	}
}

// httpReturnControl — DELETE /screen-sessions/{id}/control: оператор отдаёт управление
// сотруднику. Необратимо: взять снова — новый сеанс с новой строкой аудита.
func (s *Service) httpReturnControl(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	var returned bool
	var startedAt *time.Time
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		sess, err := s.st.get(ctx, q, id)
		if err != nil {
			return err
		}
		if !sess.Control || sess.ControlReturnedAt != nil || sess.Status == "ended" {
			return nil
		}
		startedAt = sess.StartedAt
		if returned, err = s.st.returnControl(ctx, q, id); err != nil || !returned {
			return err
		}
		off := int64(0)
		if startedAt != nil {
			off = time.Since(*startedAt).Milliseconds()
		}
		if err := s.st.inputEvent(ctx, q, id, "control_returned", "", 1, off); err != nil {
			return err
		}
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_control_returned", "screen_session", id,
			map[string]any{"device_id": sess.DeviceID})
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internal(w, "return control", err)
		return
	}
	if !returned {
		writeErr(w, http.StatusConflict, "control_not_granted")
		return
	}
	if h := s.hubs.get(id); h != nil {
		h.giveBack()
	}
	w.WriteHeader(http.StatusNoContent)
}

// flushInput пишет накопленные счётчики ввода агрегатами. Ошибка журнала не рвёт сеанс:
// это телеметрия ввода, а передача и возврат управления пишутся fail-closed отдельно.
func (r *streamRun) flushInput(ctx context.Context) {
	c := r.hub.takeCounts()
	if c.empty() {
		return
	}
	off := int64(0)
	if !r.started.IsZero() {
		off = time.Since(r.started).Milliseconds()
	}
	err := r.s.st.scoped(ctx, r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		for _, row := range []struct {
			kind string
			n    int
		}{{"click", c.clicks}, {"key", c.keys}, {"text", c.texts}, {"scroll", c.scrolls}, {"move", c.moves}} {
			if row.n == 0 {
				continue
			}
			if err := r.s.st.inputEvent(ctx, q, r.sess.ID, row.kind, "", row.n, off); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.s.log.Warn("screen: input log flush failed", "session_id", r.sess.ID, "err", err)
	}
}

type displayResult int

const (
	displayOK displayResult = iota
	displayNotYours
	displayClosed
	displayBadIndex
)

// narrowScope — что стрим пошлёт агенту по сигналу narrow: выбранный монитор и «управление
// ещё у оператора». Возврат управления и смена монитора — одно сообщение, поэтому оно всегда
// несёт оба значения: иначе смена экрана сняла бы управление, а возврат — вернула бы монитор 0.
func (b *hub) narrowScope() (displays []int32, allowInput bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	displays = []int32{int32(b.wantDisp)}
	if b.wantProfile != 0 {
		displays = append(displays, b.wantProfile) // коды ≥100 агент читает как смену качества
	}
	return displays, b.control && !b.returned
}

// profileCode — код профиля в ScreenScope.displays (см. profileCodeBase на стороне агента).
func profileCode(name string) int32 {
	switch name {
	case "low":
		return 101
	case "medium":
		return 102
	case "high":
		return 103
	case "ultra":
		return 104
	}
	return 0
}

// setProfile запоминает профиль и будит стрим.
func (b *hub) setProfile(tenantID, userID, name string) displayResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.tenantID != tenantID || b.operatorID != userID:
		return displayNotYours
	case b.closed:
		return displayClosed
	}
	code := profileCode(name)
	if code == 0 {
		return displayBadIndex
	}
	b.wantProfile = code
	select {
	case b.narrow <- struct{}{}:
	default:
	}
	return displayOK
}

// httpProfile — POST /screen-sessions/{id}/profile {"profile": "low|medium|high|\"\""}.
// Пустое значение — «авто», то есть потолок тенанта; выше потолка выбрать нельзя.
func (s *Service) httpProfile(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h := s.hubs.get(chi.URLParam(r, "id"))
	if h == nil {
		writeErr(w, http.StatusServiceUnavailable, "stream_not_open")
		return
	}
	var capName string
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		pol, err := s.st.policy(ctx, q, tenantID)
		capName = pol.ProfileCap
		return err
	})
	if err != nil {
		s.internal(w, "profile cap", err)
		return
	}
	switch h.setProfile(tenantID, userID, pickProfile(req.Profile, capName)) {
	case displayOK:
		w.WriteHeader(http.StatusNoContent)
	case displayNotYours:
		http.Error(w, "not found", http.StatusNotFound)
	case displayClosed:
		writeErr(w, http.StatusGone, "session_ended")
	default:
		writeErr(w, http.StatusBadRequest, "bad_profile")
	}
}

// setDisplay запоминает выбранный монитор и будит стрим. Допустим только номер из тех, что
// агент сообщил в кадрах (до первого кадра выбирать нечего).
func (b *hub) setDisplay(tenantID, userID string, idx int) displayResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.tenantID != tenantID || b.operatorID != userID:
		return displayNotYours
	case b.closed:
		return displayClosed
	case idx < 0 || idx >= b.displays:
		return displayBadIndex
	}
	b.wantDisp = idx
	select {
	case b.narrow <- struct{}{}:
	default:
	}
	b.requestKeyframe()
	return displayOK
}

// httpDisplay — POST /screen-sessions/{id}/display {"index": n}.
func (s *Service) httpDisplay(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h := s.hubs.get(chi.URLParam(r, "id"))
	if h == nil {
		writeErr(w, http.StatusServiceUnavailable, "stream_not_open")
		return
	}
	switch h.setDisplay(tenantID, userID, req.Index) {
	case displayOK:
		w.WriteHeader(http.StatusNoContent)
	case displayNotYours:
		http.Error(w, "not found", http.StatusNotFound)
	case displayClosed:
		writeErr(w, http.StatusGone, "session_ended")
	default:
		writeErr(w, http.StatusBadRequest, "bad_display")
	}
}
