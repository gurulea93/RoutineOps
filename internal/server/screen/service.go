//go:build screen

package screen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Floodww/RoutineOps/internal/screenframe"
	"github.com/Floodww/RoutineOps/internal/server/gateway"
	"github.com/Floodww/RoutineOps/internal/server/registry"
	"github.com/Floodww/RoutineOps/internal/server/storage"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Service — серверная сторона сеанса RSF1 (Этап A, M1: только просмотр).
//
// Инварианты, ради которых он устроен именно так:
//   - личность устройства — ТОЛЬКО отпечаток mTLS-сертификата; session_id из hello прав не
//     даёт, сервер сверяет screen_sessions.device_id (§9.18);
//   - запись «сеанс начат» в аудит фиксируется ДО SessionAccepted, и её отказ отменяет
//     сеанс: без строки аудита агент первого кадра не отправит (fail-closed);
//   - запись ведётся сервером; упор в квоту или отказ диска закрывает сеанс, потому что
//     в unattended запись — единственный способ восстановить, что видел оператор (§6);
//   - нет опроса зрителя дольше ViewerTimeout — сеанс закрывается VIEWER_GONE (§9.9).
type Service struct {
	cfg   Config
	st    store
	hubs  hubs
	log   *slog.Logger
	timer sync.WaitGroup
}

// Config — параметры сервиса. Нулевые значения заменяются умолчаниями.
type Config struct {
	DB              *storage.DB
	Registry        *registry.Registry
	RecordingDir    string
	MaxSessionBytes int64         // 0 → DefaultMaxSessionBytes
	TenantQuota     int64         // 0 → DefaultTenantQuota
	Retention       time.Duration // 0 → 30 суток
	ViewerTimeout   time.Duration // 0 → 60 с
	MaxDuration     time.Duration // 0 → 1 ч (потолок сервера; агент держит свой)
	StartTimeout    time.Duration // 0 → 2 мин: приглашение без hello
	Logger          *slog.Logger
}

func New(cfg Config) (*Service, error) {
	if cfg.DB == nil || cfg.Registry == nil {
		return nil, errors.New("screen: DB and Registry are required")
	}
	if cfg.RecordingDir == "" {
		return nil, errors.New("screen: RecordingDir is required (записи без каталога быть не может)")
	}
	if err := os.MkdirAll(cfg.RecordingDir, 0o700); err != nil {
		return nil, fmt.Errorf("screen: recording dir: %w", err)
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.Retention, 30*24*time.Hour)
	def(&cfg.ViewerTimeout, 60*time.Second)
	def(&cfg.MaxDuration, time.Hour)
	def(&cfg.StartTimeout, 2*time.Minute)
	if cfg.MaxSessionBytes <= 0 {
		cfg.MaxSessionBytes = DefaultMaxSessionBytes
	}
	if cfg.TenantQuota <= 0 {
		cfg.TenantQuota = DefaultTenantQuota
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg, st: store{db: cfg.DB}, log: cfg.Logger.With("component", "screen")}, nil
}

var (
	_ gateway.ScreenService        = (*Service)(nil)
	_ gateway.ScreenTaskResultSink = (*Service)(nil)
)

// agentActor — от чьего имени пишутся события, которые инициирует устройство.
func agentActor(deviceID string) string { return "agent:" + deviceID }

// resolveDevice — устройство и тенант по отпечатку. Отрезанное устройство не получает
// сеанса, даже если приглашение успело уехать.
func (s *Service) resolveDevice(ctx context.Context, fingerprint string) (deviceID, tenantID string, err error) {
	id, tenantID, devStatus, err := s.cfg.DB.GetDeviceTenantByFingerprint(ctx, fingerprint)
	if err != nil {
		return "", "", status.Errorf(codes.Unavailable, "device lookup: %v", err)
	}
	if id == "" || tenantID == "" {
		return "", "", status.Error(codes.Unauthenticated, "unknown device")
	}
	switch devStatus {
	case "blocked", "decommissioned", "rejected":
		return "", "", status.Error(codes.PermissionDenied, "device cut off")
	}
	return id, tenantID, nil
}

// Stream обслуживает стрим сеанса от hello до конца.
func (s *Service) Stream(fingerprint string, stream pb.AgentService_ScreenSessionServer) error {
	ctx := stream.Context()
	deviceID, tenantID, err := s.resolveDevice(ctx, fingerprint)
	if err != nil {
		return err
	}

	hello, err := recvHello(stream)
	if err != nil {
		return err
	}
	if hello.Width <= 0 || hello.Height <= 0 || hello.Width > 0xffff || hello.Height > 0xffff {
		return status.Errorf(codes.InvalidArgument, "bad geometry %dx%d", hello.Width, hello.Height)
	}

	var sess *SessionRow
	err = s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
		sess, err = s.st.get(ctx, q, hello.SessionId)
		return err
	})
	// Чужой и несуществующий сеанс неразличимы: иначе по ответу можно перебирать id.
	if errors.Is(err, ErrNotFound) || (err == nil && sess.DeviceID != deviceID) {
		return status.Error(codes.NotFound, "no such session")
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "session lookup: %v", err)
	}
	if sess.Status != "requested" {
		return status.Error(codes.FailedPrecondition, "session is not awaiting a stream")
	}

	h := newHub(uint16(hello.Width), uint16(hello.Height))
	h.setOwner(tenantID, sess)
	if !s.hubs.put(sess.ID, h) {
		return status.Error(codes.AlreadyExists, "session already has a stream")
	}
	run := &streamRun{s: s, stream: stream, sess: sess, tenantID: tenantID, deviceID: deviceID, hub: h}
	defer s.retireHub(sess.ID, h)

	if reason, detail := run.start(ctx, hello); reason != "" {
		run.finish(reason, detail)
		return nil
	}
	run.loop(ctx)
	return nil
}

func recvHello(stream pb.AgentService_ScreenSessionServer) (*pb.SessionHello, error) {
	type res struct {
		m   *pb.SessionUp
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := stream.Recv()
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if h := r.m.GetHello(); h != nil {
			return h, nil
		}
		return nil, status.Error(codes.InvalidArgument, "first message must be hello")
	case <-time.After(15 * time.Second):
		return nil, status.Error(codes.DeadlineExceeded, "no hello")
	}
}

// retireHub держит закрытый хаб ещё немного: зритель должен дочитать последние кадры и
// увидеть X-Screen-Closed с причиной, а не голый 410.
func (s *Service) retireHub(id string, h *hub) {
	s.timer.Add(1)
	time.AfterFunc(30*time.Second, func() {
		defer s.timer.Done()
		s.hubs.remove(id, h)
	})
}

// streamRun — состояние одного стрима.
type streamRun struct {
	s        *Service
	stream   pb.AgentService_ScreenSessionServer
	sess     *SessionRow
	tenantID string
	deviceID string
	hub      *hub
	rec      *rsfRecorder
	started  time.Time
	c        counters
	maxLagMs int64
}

// start: запись, аудит, активация, SessionAccepted. Непустая причина — сеанс не начался.
func (r *streamRun) start(ctx context.Context, hello *pb.SessionHello) (reason, detail string) {
	s := r.s
	var used int64
	err := s.st.scoped(ctx, r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		used, err = s.st.recordingUsed(ctx, q)
		return err
	})
	if err != nil {
		return "RECORDING_FAILED", "quota lookup: " + err.Error()
	}
	if used >= s.cfg.TenantQuota {
		r.markTruncated(truncTenantQuota)
		return "RECORDING_QUOTA", "tenant recording quota exhausted"
	}
	r.rec, err = newRSFRecorder(s.cfg.RecordingDir, r.sess.ID, s.cfg.MaxSessionBytes, s.cfg.TenantQuota-used)
	if err != nil {
		r.markTruncated(truncIOError)
		return "RECORDING_FAILED", err.Error()
	}

	// 🔴 Аудит и активация — одной транзакцией: строка «сеанс начат» и статус active
	// либо есть обе, либо нет ни одной. Accepted уходит только после коммита.
	err = s.st.scoped(ctx, r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		ok, err := s.st.activate(ctx, q, r.sess.ID, int(hello.Width), int(hello.Height), hello.ProfileFallback)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("session left 'requested' before the stream started")
		}
		return s.cfg.DB.WriteAuditLog(ctx, "", agentActor(r.deviceID), "screen_session_started", "screen_session", r.sess.ID,
			map[string]any{
				"device_id": r.deviceID, "operator_email": r.sess.OperatorEmail, "reason": r.sess.Reason,
				"mode": r.sess.Mode, "profile": r.sess.Profile, "width": hello.Width, "height": hello.Height,
				"agent_version": hello.AgentVersion, "profile_fallback": hello.ProfileFallback,
			})
	})
	if err != nil {
		s.log.Error("screen: session not started (audit/activate)", "session_id", r.sess.ID, "err", err)
		return "AUDIT_UNAVAILABLE", err.Error()
	}

	r.started = time.Now()
	if err := r.stream.Send(&pb.SessionDown{Msg: &pb.SessionDown_Accepted{
		Accepted: &pb.SessionAccepted{ServerTimeMs: r.started.UnixMilli()},
	}}); err != nil {
		return "AGENT_GONE", err.Error()
	}
	s.log.Info("screen: session started", "session_id", r.sess.ID, "device_id", r.deviceID,
		"geometry", fmt.Sprintf("%dx%d", hello.Width, hello.Height))
	return "", ""
}

func (r *streamRun) markTruncated(why string) {
	_ = r.s.st.scoped(context.WithoutCancel(r.stream.Context()), r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		return r.s.st.setRecording(ctx, q, r.sess.ID, "", 0, true, why)
	})
}

func (r *streamRun) loop(ctx context.Context) {
	s := r.s
	up := make(chan *pb.SessionUp)
	upErr := make(chan error, 1)
	go func() {
		for {
			m, err := r.stream.Recv()
			if err != nil {
				upErr <- err
				return
			}
			select {
			case up <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	maxDur := time.NewTimer(s.cfg.MaxDuration)
	defer maxDur.Stop()
	lastFlush := time.Now()

	// stopping — сервер попросил агента закончить; ждём его SessionEnd недолго.
	var stopReason string
	var stopDeadline <-chan time.Time
	stop := func(reason string) {
		if stopReason != "" {
			return
		}
		stopReason = reason
		_ = r.stream.Send(&pb.SessionDown{Msg: &pb.SessionDown_Control{
			Control: &pb.SessionControl{Stop: true, StopReason: reason},
		}})
		stopDeadline = time.After(5 * time.Second)
	}

	for {
		select {
		case m := <-up:
			switch msg := m.Msg.(type) {
			case *pb.SessionUp_Frame:
				if stopReason != "" {
					continue
				}
				if reason := r.frame(msg.Frame); reason != "" {
					stop(reason)
				}
			case *pb.SessionUp_Stats:
				st := msg.Stats
				r.c.Stalls, r.c.Dropped = max(r.c.Stalls, int64(st.Stalls)), max(r.c.Dropped, int64(st.Dropped))
			case *pb.SessionUp_Frames:
				r.hub.setFlow(msg.Frames.GetPaused(), msg.Frames.GetReason())
			case *pb.SessionUp_End:
				reason := msg.End.GetReason()
				if stopReason != "" {
					reason = stopReason // причина того, кто закрыл первым
				}
				if reason == "" {
					reason = "AGENT_GONE"
				}
				r.finish(reason, msg.End.GetDetail())
				return
			case *pb.SessionUp_Hello:
				stop("PROTOCOL_ERROR")
			default:
				// Ввод/сигнализация/защищённый стол — не в M1. Не рвём сеанс: агент новее.
			}
		case err := <-upErr:
			reason := stopReason
			if reason == "" {
				reason = "AGENT_GONE"
			}
			r.finish(reason, errDetail(err))
			return
		case <-r.hub.keyframe:
			_ = r.stream.Send(&pb.SessionDown{Msg: &pb.SessionDown_Control{
				Control: &pb.SessionControl{RequestKeyframe: true},
			}})
		case in := <-r.hub.input:
			if stopReason == "" {
				_ = r.stream.Send(&pb.SessionDown{Msg: &pb.SessionDown_Input{Input: in}})
			}
		case <-r.hub.narrow:
			// Возврат управления — сужение, а не стоп: сеанс наблюдения продолжается.
			ds, allow := r.hub.narrowScope()
			_ = r.stream.Send(&pb.SessionDown{Msg: &pb.SessionDown_Control{
				Control: &pb.SessionControl{Narrow: &pb.ScreenScope{Displays: ds, AllowInput: allow}},
			}})
		case reason := <-r.hub.stop:
			stop(reason)
		case <-maxDur.C:
			stop("MAX_DURATION")
		case <-tick.C:
			if stopReason == "" && r.hub.idleFor() > s.cfg.ViewerTimeout {
				stop("VIEWER_GONE")
			}
			if time.Since(lastFlush) >= 10*time.Second {
				lastFlush = time.Now()
				r.flushProgress(ctx)
				r.flushInput(ctx)
			}
		case <-stopDeadline:
			r.finish(stopReason, "agent did not confirm stop")
			return
		case <-ctx.Done():
			reason := stopReason
			if reason == "" {
				reason = "AGENT_GONE"
			}
			r.finish(reason, "stream context done")
			return
		}
	}
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// frame принимает часть кадра. Непустая причина — сеанс надо закрывать.
func (r *streamRun) frame(f *pb.SessionFrame) string {
	if len(f.Payload) > screenframe.MaxPartBytes {
		return "PROTOCOL_ERROR"
	}
	p, err := screenframe.ParsePart(f.Payload)
	if err != nil || p.Seq != f.Seq || p.Keyframe != f.Keyframe || p.Last != f.LastPart {
		r.s.log.Warn("screen: malformed frame part", "session_id", r.sess.ID, "err", err)
		return "PROTOCOL_ERROR"
	}
	rel := time.Since(r.started).Milliseconds()
	if rel > 0xffffffff {
		return "MAX_DURATION"
	}
	if f.CapturedAtMs > 0 {
		r.maxLagMs = max(r.maxLagMs, time.Now().UnixMilli()-f.CapturedAtMs)
	}
	rec := appendRecord(uint32(rel), f.Payload)
	// Запись — раньше хаба: кадр, который не попал в запись, зритель увидеть не должен.
	if err := r.rec.write(&p, rec); err != nil {
		return truncationEndReason(r.rec.truncated)
	}
	r.hub.append(&p, rec)
	r.c.Bytes += int64(len(f.Payload))
	if f.LastPart {
		r.c.Frames++
	}
	return ""
}

func (r *streamRun) flushProgress(ctx context.Context) {
	err := r.s.st.scoped(ctx, r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		if err := r.s.st.progress(ctx, q, r.sess.ID, r.c); err != nil {
			return err
		}
		if r.hub.idleFor() < r.s.cfg.ViewerTimeout {
			return r.s.st.touchViewer(ctx, q, r.sess.ID)
		}
		return nil
	})
	if err != nil {
		r.s.log.Warn("screen: progress flush failed", "session_id", r.sess.ID, "err", err)
	}
}

// finish закрывает сеанс: хаб, файл записи, строка журнала, аудит. Работает и после
// обрыва стрима, поэтому — без контекста запроса.
func (r *streamRun) finish(reason, detail string) {
	s := r.s
	ctx := context.WithoutCancel(r.stream.Context())
	r.hub.close(reason)
	r.flushInput(ctx)
	var name, trunc string
	var n int64
	if r.rec != nil {
		name, n, trunc = r.rec.close()
	}
	err := s.st.scoped(ctx, r.tenantID, func(ctx context.Context, q storage.TenantScope) error {
		if r.rec != nil {
			if err := s.st.setRecording(ctx, q, r.sess.ID, name, n, trunc != "", trunc); err != nil {
				return err
			}
		}
		if err := s.st.progress(ctx, q, r.sess.ID, r.c); err != nil {
			return err
		}
		_, err := s.st.end(ctx, q, r.sess.ID, "", reason, detail)
		return err
	})
	if err != nil {
		s.log.Error("screen: session close not persisted", "session_id", r.sess.ID, "err", err)
	}
	s.audit(ctx, r.tenantID, "", agentActor(r.deviceID), "screen_session_ended", r.sess.ID, map[string]any{
		"device_id": r.deviceID, "end_reason": reason, "frames": r.c.Frames, "bytes": r.c.Bytes,
		"recording_bytes": n, "recording_truncated": trunc, "max_lag_ms": r.maxLagMs,
	})
	s.log.Info("screen: session ended", "session_id", r.sess.ID, "reason", reason, "detail", detail,
		"frames", r.c.Frames, "bytes", r.c.Bytes, "recording_bytes", n, "max_lag_ms", r.maxLagMs,
		"stalls", r.c.Stalls, "dropped", r.c.Dropped)
	s.sweepRetention(ctx, r.tenantID)
}

// audit — best-effort запись для событий ПОСЛЕ факта (конец сеанса): само событие уже
// случилось, и отказ журнала его не отменит. Fail-closed точки пишут аудит сами.
func (s *Service) audit(ctx context.Context, tenantID, userID, email, action, sessionID string, details any) {
	err := s.st.scoped(ctx, tenantID, func(ctx context.Context, _ storage.TenantScope) error {
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, action, "screen_session", sessionID, details)
	})
	if err != nil {
		s.log.Error("screen: audit write failed", "action", action, "session_id", sessionID, "err", err)
	}
}

// sweepRetention удаляет записи тенанта старше срока хранения. Ленивая, на закрытии
// сеанса: подметальщику по всем тенантам пришлось бы обходить RLS.
func (s *Service) sweepRetention(ctx context.Context, tenantID string) {
	var ids, names []string
	err := s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
		var err error
		ids, names, err = s.st.expiredRecordings(ctx, q, s.cfg.Retention)
		return err
	})
	if err != nil {
		s.log.Warn("screen: retention lookup failed", "err", err)
		return
	}
	for i, name := range names {
		if err := s.removeRecording(name); err != nil {
			s.log.Error("screen: retention delete failed", "session_id", ids[i], "err", err)
			continue
		}
		_ = s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
			return s.st.dropRecording(ctx, q, ids[i])
		})
	}
}

func (s *Service) removeRecording(name string) error {
	p, err := recordingPath(s.cfg.RecordingDir, name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Event — durable-события агента (outbox): конец сеанса, исход согласия, телеметрия.
func (s *Service) Event(ctx context.Context, fingerprint string, ev *pb.ScreenEvent) (*pb.ScreenEventAck, error) {
	deviceID, tenantID, err := s.resolveDevice(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	at := time.Now()
	if ev.AtMs > 0 {
		at = time.UnixMilli(ev.AtMs)
	}
	var ended bool
	err = s.st.scoped(ctx, tenantID, func(ctx context.Context, q storage.TenantScope) error {
		switch ev.Kind {
		case "end":
			reason := strings.TrimSpace(ev.Reason)
			if reason == "" {
				reason = "AGENT_GONE"
			}
			var err error
			ended, err = s.st.end(ctx, q, ev.SessionId, deviceID, reason, ev.Detail)
			return err
		case "consent":
			switch ev.Reason {
			case "GRANTED", "DENIED", "TIMEOUT", "UNAVAILABLE":
				return s.st.consent(ctx, q, ev.SessionId, deviceID, ev.Reason, at)
			}
			return nil // неизвестное состояние в журнал не кладём (CHECK из 068)
		case "telemetry":
			st := ev.GetStats()
			if st == nil {
				return nil
			}
			return s.st.telemetry(ctx, q, ev.SessionId, deviceID, at, counters{
				Frames: int64(st.Frames), Bytes: int64(st.Bytes), Stalls: int64(st.Stalls), Dropped: int64(st.Dropped)})
		}
		return nil
	})
	if err != nil {
		// Невалидный UUID и т.п. — ack, а не ошибка: иначе отравленное событие встанет в
		// голове durable-очереди агента навсегда.
		s.log.Warn("screen: event not applied", "kind", ev.Kind, "session_id", ev.SessionId, "err", err)
		return &pb.ScreenEventAck{Received: true}, nil
	}
	if ended {
		if h := s.hubs.get(ev.SessionId); h != nil {
			h.Stop(ev.Reason)
		}
	}
	return &pb.ScreenEventAck{Received: true}, nil
}

// TaskResult — исход задачи-приглашения. Успех ничего не меняет (дальше придёт стрим);
// отказ закрывает сеанс кодом агента.
func (s *Service) TaskResult(ctx context.Context, deviceID, taskID string, failed bool, refusal, errorLog string) {
	if !failed {
		return
	}
	reason := strings.TrimSpace(refusal)
	if reason == "" {
		reason = "AGENT_GONE"
	}
	// Скоуп уже открыт gateway (тенант устройства): Scoped берёт его транзакцию.
	q := s.cfg.DB.Scoped(ctx)
	var id string
	if err := q.QueryRow(ctx, `SELECT id::text FROM screen_sessions WHERE task_id = $1::uuid AND device_id = $2::uuid`,
		taskID, deviceID).Scan(&id); err != nil {
		s.log.Warn("screen: refusal for unknown session", "task_id", taskID, "err", err)
		return
	}
	if _, err := s.st.end(ctx, q, id, deviceID, reason, errorLog); err != nil {
		s.log.Error("screen: refusal not persisted", "session_id", id, "err", err)
		return
	}
	s.log.Info("screen: agent refused session", "session_id", id, "reason", reason)
}
