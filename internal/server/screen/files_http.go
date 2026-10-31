//go:build screen

package screen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Floodww/RoutineOps/internal/server/storage"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Файловый менеджер сеанса с управлением (как в RustDesk). Сервер — посредник между
// браузером оператора (HTTPS) и агентом (стрим сеанса, сигналы SessionSignal.ice с
// sdp_mid="fs", см. agent/screen/files.go). Файлы на сервере не хранятся: идут потоком.
// Каждый перенос — строка аудита: кто, какой файл, куда, сколько байт.

const (
	filesMid      = "fs"
	fsChunk       = 96 << 10 // как у агента
	fsUpWindow    = 8        // кусков загрузки в полёте без подтверждения
	fsReplyWait   = 30 * time.Second
	fsMaxUpload   = 4 << 30
	fsTransferMax = 6 * time.Hour
)

type fsReq struct {
	ID   string `json:"id"`
	Op   string `json:"op"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
	Off  int64  `json:"off,omitempty"`
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof,omitempty"`
}

type fsEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

type fsResp struct {
	ID      string    `json:"id"`
	OK      bool      `json:"ok"`
	Err     string    `json:"err,omitempty"`
	Path    string    `json:"path,omitempty"`
	Entries []fsEntry `json:"entries,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Off     int64     `json:"off,omitempty"`
	Data    []byte    `json:"data,omitempty"`
	EOF     bool      `json:"eof,omitempty"`
}

// fsRouter — ответы агента по id запроса.
type fsRouter struct {
	mu   sync.Mutex
	wait map[string]chan fsResp
}

var errFsGone = errors.New("session ended")

func (b *hub) fsRoute() *fsRouter {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fs == nil {
		b.fs = &fsRouter{wait: map[string]chan fsResp{}}
	}
	return b.fs
}

// deliverFS — ответ агента (из стрима сеанса). Не блокируется: окно подтверждений держит
// очередь короче ёмкости канала; лишнее (никто не ждёт) выбрасывается.
func (b *hub) deliverFS(raw string) {
	var r fsResp
	if json.Unmarshal([]byte(raw), &r) != nil || r.ID == "" {
		return
	}
	rt := b.fsRoute()
	rt.mu.Lock()
	ch := rt.wait[r.ID]
	rt.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- r:
	default:
	}
}

func (rt *fsRouter) open(id string) chan fsResp {
	ch := make(chan fsResp, 64)
	rt.mu.Lock()
	rt.wait[id] = ch
	rt.mu.Unlock()
	return ch
}

func (rt *fsRouter) done(id string) {
	rt.mu.Lock()
	delete(rt.wait, id)
	rt.mu.Unlock()
}

// sendFS кладёт запрос агенту, ожидая места в очереди сигналов.
func (b *hub) sendFS(ctx context.Context, r fsReq) error {
	j, err := json.Marshal(r)
	if err != nil {
		return err
	}
	sig := &pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{Candidate: string(j), SdpMid: filesMid}}}
	for {
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return errFsGone
		}
		select {
		case b.sig <- sig:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func fsWaitReply(ctx context.Context, ch chan fsResp) (fsResp, error) {
	t := time.NewTimer(fsReplyWait)
	defer t.Stop()
	select {
	case r := <-ch:
		if !r.OK {
			return r, errors.New(r.Err)
		}
		return r, nil
	case <-t.C:
		return fsResp{}, errors.New("agent did not answer")
	case <-ctx.Done():
		return fsResp{}, ctx.Err()
	}
}

func newFsID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// fsHub — хаб сеанса, если вызывающий — его оператор и управление у него.
func (s *Service) fsHub(w http.ResponseWriter, r *http.Request) (h *hub, tenantID, userID, email string, ok bool) {
	tenantID, userID, email, ok = actor(w, r)
	if !ok {
		return nil, "", "", "", false
	}
	h = s.hubs.get(chi.URLParam(r, "id"))
	if h == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, "", "", "", false
	}
	h.mu.Lock()
	mine := h.tenantID == tenantID && h.operatorID == userID
	ctl := h.control && !h.returned && !h.closed
	h.mu.Unlock()
	if !mine {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, "", "", "", false
	}
	if !ctl {
		writeErr(w, http.StatusConflict, "control_not_granted")
		return nil, "", "", "", false
	}
	return h, tenantID, userID, email, true
}

func (s *Service) fsAudit(ctx context.Context, tenantID, userID, email, action, sessionID string, details map[string]any) {
	if err := s.st.scoped(ctx, tenantID, func(ctx context.Context, _ storage.TenantScope) error {
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, action, "screen_session", sessionID, details)
	}); err != nil {
		s.log.Error("screen: file audit failed", "action", action, "err", err)
	}
}

// httpFsList — GET /screen-sessions/{id}/files?path=… (пусто — диски; home=1 — Рабочий стол).
func (s *Service) httpFsList(w http.ResponseWriter, r *http.Request) {
	h, _, _, _, ok := s.fsHub(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if r.URL.Query().Get("home") == "1" {
		resp, err := s.fsCall(r.Context(), h, fsReq{Op: "home"})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "agent_error")
			return
		}
		p = resp.Path
	}
	resp, err := s.fsCall(r.Context(), h, fsReq{Op: "list", Path: p})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"path": p, "error": err.Error(), "entries": []fsEntry{}})
		return
	}
	if resp.Entries == nil {
		resp.Entries = []fsEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": resp.Entries})
}

// httpFsMkdir — POST /screen-sessions/{id}/files/mkdir {path, name}.
func (s *Service) httpFsMkdir(w http.ResponseWriter, r *http.Request) {
	h, tenantID, userID, email, ok := s.fsHub(w, r)
	if !ok {
		return
	}
	var req struct{ Path, Name string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil || req.Path == "" || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "path_and_name_required")
		return
	}
	resp, err := s.fsCall(r.Context(), h, fsReq{Op: "mkdir", Path: req.Path, Name: req.Name})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.fsAudit(r.Context(), tenantID, userID, email, "screen_folder_created", chi.URLParam(r, "id"),
		map[string]any{"path": resp.Path})
	writeJSON(w, http.StatusCreated, map[string]string{"path": resp.Path})
}

func (s *Service) fsCall(ctx context.Context, h *hub, req fsReq) (fsResp, error) {
	rt := h.fsRoute()
	req.ID = newFsID()
	ch := rt.open(req.ID)
	defer rt.done(req.ID)
	if err := h.sendFS(ctx, req); err != nil {
		return fsResp{}, err
	}
	return fsWaitReply(ctx, ch)
}

// httpFsDownload — GET /screen-sessions/{id}/files/download?path=… — файл с машины
// сотрудника потоком.
func (s *Service) httpFsDownload(w http.ResponseWriter, r *http.Request) {
	h, tenantID, userID, email, ok := s.fsHub(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, http.StatusBadRequest, "path_required")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(fsTransferMax))

	rt := h.fsRoute()
	id := newFsID()
	ch := rt.open(id)
	defer rt.done(id)
	ctx := r.Context()
	if err := h.sendFS(ctx, fsReq{ID: id, Op: "read", Path: p}); err != nil {
		writeErr(w, http.StatusBadGateway, "agent_unreachable")
		return
	}
	first, err := fsWaitReply(ctx, ch)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	name := path.Base(strings.ReplaceAll(p, "\\", "/"))
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	hd.Set("Content-Length", strconv.FormatInt(first.Size, 10))
	w.WriteHeader(http.StatusOK)

	var sent int64
	complete := false
	defer func() {
		if !complete {
			_ = h.sendFS(context.Background(), fsReq{ID: id, Op: "cancel"})
		}
		s.fsAudit(context.Background(), tenantID, userID, email, "screen_file_downloaded", chi.URLParam(r, "id"),
			map[string]any{"path": p, "bytes": sent, "size": first.Size, "complete": complete})
	}()
	cur := first
	for {
		if _, err := w.Write(cur.Data); err != nil {
			return
		}
		sent += int64(len(cur.Data))
		if cur.EOF {
			complete = sent == first.Size
			_ = rc.Flush()
			return
		}
		if err := h.sendFS(ctx, fsReq{ID: id, Op: "ack"}); err != nil {
			return
		}
		if cur, err = fsWaitReply(ctx, ch); err != nil {
			return
		}
	}
}

// httpFsUpload — POST /screen-sessions/{id}/files/upload?path=<папка>&name=<имя>, тело —
// сам файл (application/octet-stream), потоком.
func (s *Service) httpFsUpload(w http.ResponseWriter, r *http.Request) {
	h, tenantID, userID, email, ok := s.fsHub(w, r)
	if !ok {
		return
	}
	dir, name := r.URL.Query().Get("path"), r.URL.Query().Get("name")
	if dir == "" || name == "" {
		writeErr(w, http.StatusBadRequest, "path_and_name_required")
		return
	}
	if r.ContentLength > fsMaxUpload {
		writeErr(w, http.StatusRequestEntityTooLarge, "file_too_large")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(fsTransferMax))
	_ = rc.SetWriteDeadline(time.Now().Add(fsTransferMax))
	body := http.MaxBytesReader(w, r.Body, fsMaxUpload)

	rt := h.fsRoute()
	id := newFsID()
	ch := rt.open(id)
	defer rt.done(id)
	ctx := r.Context()

	var (
		off      int64
		inflight int
		dest     string
		finished bool
		failErr  error
	)
	defer func() {
		if !finished {
			_ = h.sendFS(context.Background(), fsReq{ID: id, Op: "cancel"})
		}
		s.fsAudit(context.Background(), tenantID, userID, email, "screen_file_uploaded", chi.URLParam(r, "id"),
			map[string]any{"path": dir, "name": name, "dest": dest, "bytes": off, "complete": finished})
	}()
	buf := make([]byte, fsChunk)
	for {
		n, rerr := io.ReadFull(body, buf)
		eof := errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF)
		if rerr != nil && !eof {
			failErr = fmt.Errorf("upload interrupted: %w", rerr)
			break
		}
		req := fsReq{ID: id, Op: "write", Off: off, Data: append([]byte(nil), buf[:n]...), EOF: eof}
		if off == 0 {
			req.Path, req.Name = dir, name
		}
		if err := h.sendFS(ctx, req); err != nil {
			failErr = err
			break
		}
		off += int64(n)
		inflight++
		// Окно: не больше fsUpWindow неподтверждённых кусков у агента.
		for inflight >= fsUpWindow || (eof && inflight > 0) {
			resp, err := fsWaitReply(ctx, ch)
			if err != nil {
				failErr = err
				break
			}
			inflight--
			if resp.EOF {
				dest, finished = resp.Path, true
			}
		}
		if failErr != nil || eof {
			break
		}
	}
	if failErr != nil || !finished {
		msg := "upload failed"
		if failErr != nil {
			msg = failErr.Error()
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": dest, "bytes": off})
}
