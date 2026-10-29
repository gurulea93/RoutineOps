//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/Floodww/RoutineOps/internal/server/api"
	"github.com/Floodww/RoutineOps/internal/server/storage"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Ручки панели (web/src/components/ScreenSessionPanel.tsx, pages/ScreenAccess.tsx).
//
// Разведение по группам:
//   - все роли: журнал устройства и «есть ли у меня грант» — наблюдение;
//   - it_admin: остановка (защитное действие — автоматике не запрещаем), настройки и
//     список грантов на чтение;
//   - it_admin + человек: начать сеанс, смотреть, скачать запись, менять гранты и режим.
//     Смотреть экран сотрудника от имени сервисного токена нельзя: в журнале оказался бы
//     человек, который ничего не смотрел.

// Routes — опции роутера. Purger едет вместе с ручками: подключить удалённый стол и
// забыть про удаление его записей должно быть невозможно (api/screen_purge.go).
func (s *Service) Routes() []api.RouterOption {
	return []api.RouterOption{
		api.WithRoutes(func(_ *api.Handler, r chi.Router) {
			r.Get("/devices/{id}/screen-sessions", s.httpList)
			r.Get("/screen-recording-grants/me", s.httpMyGrant)
		}),
		api.WithAdminRoutes(func(_ *api.Handler, r chi.Router) {
			r.Post("/screen-sessions/{id}/stop", s.httpStop)
			r.Get("/screen-access-mode", s.httpPolicy)
			r.Get("/screen-recording-grants", s.httpGrants)
			// Вернуть управление — защитное действие, его не запрещаем и автоматике.
			r.Delete("/screen-sessions/{id}/control", s.httpReturnControl)
		}),
		api.WithHumanAdminRoutes(func(_ *api.Handler, r chi.Router) {
			r.Post("/devices/{id}/screen-sessions", s.httpCreate)
			r.Get("/screen-sessions/{id}/live", s.httpLive)
			// Ввод — только от человека: в журнале должен оказаться тот, кто нажимал.
			r.Post("/screen-sessions/{id}/input", s.httpInput)
			r.Get("/screen-sessions/{id}/recording", s.httpRecording)
			r.Delete("/screen-sessions/{id}/recording", s.httpDeleteRecording)
			r.Delete("/screen-sessions/{id}", s.httpDeleteSession)
			r.Put("/screen-recording-grants/{user_id}", s.httpSetGrant)
			r.Patch("/screen-access-mode", s.httpSetMode)
			r.Patch("/screen-profile-cap", s.httpSetProfileCap)
		}),
		api.WithScreenPurger(s),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode string) {
	writeJSON(w, code, map[string]string{"error": errCode})
}

// actor — тенант и пользователь запроса. ok=false — уже ответили.
func actor(w http.ResponseWriter, r *http.Request) (tenantID, userID, email string, ok bool) {
	tenantID, ok1 := api.ActorTenant(r.Context())
	userID, email, ok2 := api.Actor(r.Context())
	if !ok1 || !ok2 {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", "", "", false
	}
	return tenantID, userID, email, true
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error("screen: "+what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Service) httpList(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, ok := actor(w, r)
	if !ok {
		return
	}
	deviceID := chi.URLParam(r, "id")
	var out []*SessionRow
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		if _, err := s.st.device(ctx, q, deviceID); err != nil {
			return err
		}
		s.reapStale(ctx, q, deviceID)
		var err error
		out, err = s.st.listByDevice(ctx, q, deviceID, 50)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internal(w, "list sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// reapStale закрывает незакрытые сеансы устройства, у которых нет стрима в этом процессе:
// приглашение без hello дольше StartTimeout и «active» без хаба (сервер перезапускали).
// Без этого журнал показывал бы «идёт» сеанс, которого нет, и не давал начать новый.
func (s *Service) reapStale(ctx context.Context, q storage.TenantScope, deviceID string) {
	live, err := s.st.liveOnDevice(ctx, q, deviceID)
	if err != nil {
		return
	}
	for _, sess := range live {
		if s.hubs.get(sess.ID) != nil {
			continue
		}
		if sess.Status == "requested" && time.Since(sess.CreatedAt) < s.cfg.StartTimeout {
			continue
		}
		_, _ = s.st.end(ctx, q, sess.ID, "", "AGENT_GONE", "no stream on this server")
	}
}

type createReq struct {
	Reason      string `json:"reason"`
	Control     bool   `json:"control"`
	Maintenance bool   `json:"maintenance"`
}

const maxReasonLen = 500

func (s *Service) httpCreate(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	deviceID := chi.URLParam(r, "id")
	var req createReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Повод обязателен (§4) и показывается сотруднику на плашке.
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > maxReasonLen {
		writeErr(w, http.StatusBadRequest, "reason_required")
		return
	}

	// Обслуживание подразумевает управление (proto: maintenance ⇒ allow_input).
	if req.Maintenance {
		req.Control = true
	}

	var (
		sess    *SessionRow
		cn      string
		pbTask  *pb.Task
		errCode string
	)
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		dev, err := s.st.device(ctx, q, deviceID)
		if err != nil {
			return err
		}
		if err := s.cfg.DB.AssertAgentSupports(ctx, deviceID, "screen_session"); err != nil {
			if errors.Is(err, storage.ErrAgentTooOld) {
				errCode = "agent_unsupported"
				return nil
			}
			return err
		}
		// Версии мало: free-сборка той же версии удалённого стола не содержит (§9.17).
		switch {
		case !has(dev.Capabilities, "screen_session"):
			errCode = "agent_unsupported"
		case req.Maintenance && !has(dev.Capabilities, "screen_maintenance"):
			errCode = "agent_no_maintenance"
		case (req.Control || req.Maintenance) && !has(dev.Capabilities, "screen_control"):
			errCode = "agent_no_control"
		case dev.CN == "" || !s.cfg.Registry.Connected(dev.CN) || dev.Status != "active":
			errCode = "device_offline"
		}
		if errCode != "" {
			return nil
		}
		s.reapStale(ctx, q, deviceID)
		if live, err := s.st.liveOnDevice(ctx, q, deviceID); err != nil {
			return err
		} else if len(live) > 0 {
			errCode = "session_busy"
			return nil
		}
		pol, err := s.st.policy(ctx, q, tenantID)
		if err != nil {
			return err
		}
		mode := pol.Mode
		if mode != "consent_required" {
			mode = "unattended"
		}
		// Профиль — потолок тенанта (075); неизвестное значение — low.
		profile := "low"
		switch pol.ProfileCap {
		case "medium", "high":
			profile = pol.ProfileCap
		}
		sid, taskID, err := s.st.create(ctx, q, newSession{
			DeviceID: deviceID, OperatorID: userID, OperatorEmail: email,
			Reason: req.Reason, Mode: mode, Profile: profile, Control: req.Control,
		})
		if err != nil {
			return err
		}
		// Аудит в той же транзакции: сеанса без строки «запрошен» не существует.
		if err := s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_session_requested", "screen_session", sid,
			map[string]any{"device_id": deviceID, "reason": req.Reason, "mode": mode, "profile": profile,
				"control": req.Control, "maintenance": req.Maintenance}); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		// Передача управления — отдельная строка журнала ввода (§9.21 п.3): окно
		// неатрибутируемости считается от неё и до возврата.
		if req.Control {
			if err := s.st.inputEvent(ctx, q, sid, "control_granted", "", 1, 0); err != nil {
				return fmt.Errorf("input log: %w", err)
			}
		}
		sess, err = s.st.get(ctx, q, sid)
		if err != nil {
			return err
		}
		cn = dev.CN
		pbTask = &pb.Task{
			TaskId:   taskID,
			Platform: dev.OS,
			ScreenSession: &pb.ScreenSessionCommand{
				SessionId:         sid,
				Profile:           profile,
				ConsentRequired:   mode == "consent_required",
				AllowInput:        req.Control,
				Maintenance:       req.Maintenance,
				OperatorName:      email,
				Reason:            req.Reason,
				MaxDurationSec:    int32(s.cfg.MaxDuration / time.Second),
				ConsentTimeoutSec: 60,
				Transport:         "rsf1",
			},
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internal(w, "create session", err)
		return
	}
	if errCode != "" {
		writeErr(w, http.StatusConflict, errCode)
		return
	}

	if !s.cfg.Registry.Send(cn, pbTask) {
		s.endNow(context.WithoutCancel(r.Context()), tenantID, sess.ID, "AGENT_GONE", "invitation not delivered")
		writeErr(w, http.StatusConflict, "device_offline")
		return
	}
	s.watchStart(tenantID, sess.ID)
	writeJSON(w, http.StatusCreated, sess)
}

// watchStart закрывает приглашение, на которое агент так и не открыл стрим.
func (s *Service) watchStart(tenantID, sessionID string) {
	time.AfterFunc(s.cfg.StartTimeout, func() {
		if s.hubs.get(sessionID) != nil {
			return
		}
		ctx := context.Background()
		_ = s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
			sess, err := s.st.get(ctx, q, sessionID)
			if err != nil || sess.Status != "requested" {
				return err
			}
			_, err = s.st.end(ctx, q, sessionID, "", "AGENT_GONE", "agent did not open the stream")
			return err
		})
	})
}

func (s *Service) endNow(ctx context.Context, tenantID, sessionID, reason, detail string) {
	err := s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
		_, err := s.st.end(ctx, q, sessionID, "", reason, detail)
		return err
	})
	if err != nil {
		s.log.Error("screen: end session", "session_id", sessionID, "err", err)
	}
}

func (s *Service) httpLive(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	waitMs, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait := min(max(time.Duration(waitMs)*time.Millisecond, 0), MaxWait)

	var sess *SessionRow
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		sess, err = s.st.get(ctx, q, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internal(w, "live lookup", err)
		return
	}
	h := s.hubs.get(id)
	if h == nil {
		switch sess.Status {
		case "requested":
			w.WriteHeader(http.StatusAccepted)
		case "active":
			// Строка «active» без хаба — стрим жил в другом процессе, которого больше нет.
			s.endNow(r.Context(), tenantID, id, "AGENT_GONE", "no stream on this server")
			http.Error(w, "gone", http.StatusGone)
		default:
			http.Error(w, "gone", http.StatusGone)
		}
		return
	}
	// Вход зрителя — в аудит ДО первого кадра, fail-closed: кто смотрел экран сотрудника,
	// обязано быть известно, а не выводиться из того, кто сеанс начал.
	if since == 0 {
		err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, _ storage.TenantScope) error {
			return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_session_viewed", "screen_session", id,
				map[string]any{"device_id": sess.DeviceID})
		})
		if err != nil {
			s.internal(w, "viewer audit", err)
			return
		}
	}

	res := h.poll(r.Context(), since, wait)
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Screen-Width", strconv.Itoa(int(res.W)))
	hd.Set("X-Screen-Height", strconv.Itoa(int(res.H)))
	hd.Set("X-Screen-Seq", strconv.FormatUint(res.Next, 10))
	switch {
	case res.Paused && res.PauseWhy != "":
		hd.Set("X-Screen-Frames", "paused:"+res.PauseWhy)
	case res.Paused:
		hd.Set("X-Screen-Frames", "paused")
	default:
		hd.Set("X-Screen-Frames", "flowing")
	}
	if res.Closed {
		hd.Set("X-Screen-Closed", "1")
		hd.Set("X-Screen-End-Reason", res.EndReason)
	}
	hd.Set("Content-Length", strconv.Itoa(len(res.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Body)
}

func (s *Service) httpStop(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	var sess *SessionRow
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		sess, err = s.st.get(ctx, q, id)
		if err != nil {
			return err
		}
		// Без живого стрима закрывать некому, кроме нас.
		if sess.Status != "ended" && s.hubs.get(id) == nil {
			_, err = s.st.end(ctx, q, id, "", "OPERATOR_STOPPED", "")
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.internal(w, "stop session", err)
		return
	}
	if h := s.hubs.get(id); h != nil {
		h.Stop("OPERATOR_STOPPED")
	}
	s.audit(r.Context(), tenantID, userID, email, "screen_session_stop", id, map[string]any{"device_id": sess.DeviceID})
	w.WriteHeader(http.StatusNoContent)
}

// httpDeleteSession удаляет завершённый сеанс целиком: файл записи, строку журнала и
// журнал ввода. Идущий сеанс не трогаем — его сначала останавливают. Что именно и чьё
// удалено, остаётся в аудите: сама строка исчезает, а след — нет.
func (s *Service) httpDeleteSession(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		sess, err := s.st.get(ctx, q, id)
		if err != nil {
			return err
		}
		if sess.Status != "ended" {
			return errSessionActive
		}
		if sess.recordingPath != "" {
			if err := s.removeRecording(sess.recordingPath); err != nil {
				return err
			}
		}
		if err := s.st.deleteSession(ctx, q, id); err != nil {
			return err
		}
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_session_deleted", "screen_session", id,
			map[string]any{
				"device_id":      sess.DeviceID,
				"operator_email": sess.OperatorEmail,
				"reason":         sess.Reason,
				"control":        sess.Control,
				"had_recording":  sess.recordingPath != "",
			})
	})
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errSessionActive):
		writeErr(w, http.StatusConflict, "session_active")
	case err != nil:
		s.internal(w, "delete session", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var errSessionActive = errors.New("screen: session is not ended")

// httpDeleteRecording удаляет файл записи завершённого сеанса; строка журнала остаётся.
// Права на просмотр не требует: удаление не раскрывает содержимое, а админу, который
// сам не вправе смотреть, всё равно нужно уметь освободить место. Факт — в аудит.
func (s *Service) httpDeleteRecording(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		sess, err := s.st.get(ctx, q, id)
		if err != nil {
			return err
		}
		if sess.recordingPath == "" || sess.Status != "ended" {
			return ErrNotFound
		}
		if err := s.removeRecording(sess.recordingPath); err != nil {
			return err
		}
		if err := s.st.dropRecording(ctx, q, id); err != nil {
			return err
		}
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_recording_deleted", "screen_session", id,
			map[string]any{"device_id": sess.DeviceID, "recording_bytes": sess.RecordingBytes})
	})
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		s.internal(w, "delete recording", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Service) httpRecording(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	var (
		sess    *SessionRow
		allowed bool
	)
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		if allowed, err = s.st.canView(ctx, q, userID); err != nil || !allowed {
			return err
		}
		if sess, err = s.st.get(ctx, q, id); err != nil {
			return err
		}
		if sess.recordingPath == "" || sess.Status != "ended" {
			return ErrNotFound
		}
		// Сам факт просмотра — в аудит, fail-closed, в той же транзакции.
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_recording_viewed", "screen_session", id,
			map[string]any{"device_id": sess.DeviceID, "recording_bytes": sess.RecordingBytes})
	})
	switch {
	case err == nil && !allowed:
		writeErr(w, http.StatusForbidden, "recording_grant_required")
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		s.internal(w, "recording access", err)
		return
	}
	p, err := recordingPath(s.cfg.RecordingDir, sess.recordingPath)
	if err != nil {
		s.internal(w, "recording path", err)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.internal(w, "open recording", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.internal(w, "stat recording", err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="screen-%s.rsf"`, id))
	hd.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	// WriteTimeout сервера (60 с) рассчитан на JSON; запись до 512 МБ в него не влезает.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Minute))
	_, _ = io.Copy(w, f)
}

func (s *Service) httpMyGrant(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, ok := actor(w, r)
	if !ok {
		return
	}
	var can bool
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		can, err = s.st.canView(ctx, q, userID)
		return err
	})
	if err != nil {
		s.internal(w, "my grant", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"can_view_session_recording": can})
}

func (s *Service) httpGrants(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, ok := actor(w, r)
	if !ok {
		return
	}
	var out []grant
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		out, err = s.st.grants(ctx, q)
		return err
	})
	if err != nil {
		s.internal(w, "list grants", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) httpSetGrant(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	target := chi.URLParam(r, "user_id")
	var req struct {
		CanView bool `json:"can_view_session_recording"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Выдать право смотреть записи самому себе нельзя, пока в тенанте есть другой
	// it_admin: разделение обязанностей. Единственному администратору (домашняя лаборатория,
	// малый офис) выдать больше некому, и без исключения записи были бы недоступны вовсе;
	// такая выдача пишется в аудит с пометкой self_grant. Отозвать у себя — можно всегда.
	selfGrant := req.CanView && target == userID
	var g *grant
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		if selfGrant {
			n, err := s.st.adminCount(ctx, q)
			if err != nil {
				return err
			}
			if n > 1 {
				return errSelfGrant
			}
		}
		var err error
		if g, err = s.st.setGrant(ctx, q, target, req.CanView); err != nil {
			return err
		}
		if req.CanView && !g.CanView {
			return errViewerRole
		}
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "screen_recording_grant", "user", target,
			map[string]any{"can_view_session_recording": g.CanView, "self_grant": selfGrant})
	})
	switch {
	case errors.Is(err, errSelfGrant):
		writeErr(w, http.StatusForbidden, "self_grant")
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errViewerRole):
		writeErr(w, http.StatusConflict, "grant_requires_it_admin")
	case err != nil:
		s.internal(w, "set grant", err)
	default:
		writeJSON(w, http.StatusOK, g)
	}
}

var errSelfGrant = errors.New("screen: self grant while another it_admin exists")

var errViewerRole = errors.New("screen: grant requires it_admin")

func (s *Service) httpPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, ok := actor(w, r)
	if !ok {
		return
	}
	var (
		pol  tenantPolicy
		used int64
	)
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		if pol, err = s.st.policy(ctx, q, tenantID); err != nil {
			return err
		}
		used, err = s.st.recordingUsed(ctx, q)
		return err
	})
	if err != nil {
		s.internal(w, "policy", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":                pol.Mode,
		"profile_cap":         pol.ProfileCap,
		"recording_used":      used,
		"recording_quota":     s.cfg.TenantQuota,
		"recording_retention": int(s.cfg.Retention / (24 * time.Hour)),
	})
}

func (s *Service) httpSetMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	s.setTenantValue(w, r, &req, func() (string, bool) {
		return req.Mode, req.Mode == "unattended" || req.Mode == "consent_required"
	}, "screen_access_mode_changed", s.st.setMode)
}

func (s *Service) httpSetProfileCap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProfileCap string `json:"profile_cap"`
	}
	s.setTenantValue(w, r, &req, func() (string, bool) {
		return req.ProfileCap, req.ProfileCap == "low" || req.ProfileCap == "medium" || req.ProfileCap == "high"
	}, "screen_profile_cap_changed", s.st.setProfileCap)
}

// setTenantValue — общий путь PATCH настройки тенанта: разбор, проверка, запись и аудит
// одной транзакцией.
func (s *Service) setTenantValue(w http.ResponseWriter, r *http.Request, req any, valid func() (string, bool),
	action string, set func(context.Context, storage.TenantScope, string, string) error) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	v, ok := valid()
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_value")
		return
	}
	err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, q storage.TenantScope) error {
		if err := set(ctx, q, tenantID, v); err != nil {
			return err
		}
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, action, "tenant", tenantID, map[string]any{"value": v})
	})
	if err != nil {
		s.internal(w, action, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PurgeDevice / PurgeTenant — api.ScreenPurger: файлы записей удаляются ДО строк (их
// каскад унесёт единственную ссылку на файл). Отказ удаления отменяет операцию.
func (s *Service) PurgeDevice(ctx context.Context, tenantID, deviceID string) error {
	return s.purge(ctx, tenantID, deviceID)
}

func (s *Service) PurgeTenant(ctx context.Context, tenantID string) error {
	return s.purge(ctx, tenantID, "")
}

func (s *Service) purge(ctx context.Context, tenantID, deviceID string) error {
	var names []string
	err := s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		names, err = s.st.recordingsOf(ctx, q, deviceID)
		return err
	})
	if err != nil {
		return fmt.Errorf("screen purge: list: %w", err)
	}
	for _, n := range names {
		if err := s.removeRecording(n); err != nil {
			return fmt.Errorf("screen purge: %w", err)
		}
	}
	return nil
}

var _ api.ScreenPurger = (*Service)(nil)
