//go:build screen

package screen

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Floodww/RoutineOps/internal/server/storage"
)

// Хранилище сеансов (Этап A). Все запросы — через TenantScope: screen_sessions, tasks,
// devices и users под FORCE RLS, и мимо-скоупный запрос дал бы тихий ноль строк.
// tenants — глобальная таблица; к ней ходим той же транзакцией с явным id тенанта.

// SessionRow — строка журнала в форме, которую ждёт панель (web/src/lib/api.ts ScreenSession).
type SessionRow struct {
	ID                       string     `json:"id"`
	DeviceID                 string     `json:"device_id"`
	OperatorID               *string    `json:"operator_id"`
	OperatorEmail            string     `json:"operator_email"`
	Reason                   string     `json:"reason"`
	Mode                     string     `json:"mode"`
	Profile                  string     `json:"profile"`
	Status                   string     `json:"status"`
	EndReason                *string    `json:"end_reason"`
	EndDetail                *string    `json:"end_detail"`
	TaskID                   *string    `json:"task_id"`
	Width                    *int       `json:"width"`
	Height                   *int       `json:"height"`
	Frames                   int64      `json:"frames"`
	Bytes                    int64      `json:"bytes"`
	HasRecording             bool       `json:"has_recording"`
	RecordingTruncated       bool       `json:"recording_truncated"`
	RecordingTruncatedReason string     `json:"recording_truncated_reason"`
	RecordingBytes           int64      `json:"recording_bytes"`
	ProfileFallback          bool       `json:"profile_fallback"`
	TelemetryAt              *time.Time `json:"telemetry_at"`
	CreatedAt                time.Time  `json:"created_at"`
	StartedAt                *time.Time `json:"started_at"`
	EndedAt                  *time.Time `json:"ended_at"`
	ConsentState             *string    `json:"consent_state"`
	ConsentAt                *time.Time `json:"consent_at"`
	Control                  bool       `json:"control"`
	ControlReturnedAt        *time.Time `json:"control_returned_at"`
	// RecordingFormat — "rsf" (тайлы, играет RecordingPlayer) или "webm" (видео, <video>).
	RecordingFormat string `json:"recording_format,omitempty"`

	recordingPath string
}

const sessionCols = `id::text, device_id::text, operator_id::text, operator_email, reason, mode, profile,
	status, end_reason, end_detail, task_id::text, width, height, frames, bytes,
	recording_path, recording_truncated, recording_truncated_reason, recording_bytes,
	profile_fallback, telemetry_at, created_at, started_at, ended_at, consent_state, consent_at,
	control, control_returned_at`

func scanSession(row pgx.Row) (*SessionRow, error) {
	var s SessionRow
	var path *string
	err := row.Scan(&s.ID, &s.DeviceID, &s.OperatorID, &s.OperatorEmail, &s.Reason, &s.Mode, &s.Profile,
		&s.Status, &s.EndReason, &s.EndDetail, &s.TaskID, &s.Width, &s.Height, &s.Frames, &s.Bytes,
		&path, &s.RecordingTruncated, &s.RecordingTruncatedReason, &s.RecordingBytes,
		&s.ProfileFallback, &s.TelemetryAt, &s.CreatedAt, &s.StartedAt, &s.EndedAt, &s.ConsentState, &s.ConsentAt,
		&s.Control, &s.ControlReturnedAt)
	if err != nil {
		return nil, err
	}
	if path != nil && *path != "" {
		s.recordingPath = *path
		s.RecordingFormat = "rsf"
		if strings.HasSuffix(*path, ".webm") {
			s.RecordingFormat = "webm"
		}
		s.HasRecording = s.RecordingBytes > 0
	}
	return &s, nil
}

// ErrNotFound — сеанса нет в скоупе тенанта (или он чужой: под RLS это неразличимо).
var ErrNotFound = errors.New("screen: session not found")

type store struct{ db *storage.DB }

// scoped выполняет fn в транзакции тенанта. commit — только при nil-ошибке.
func (st store) scoped(ctx context.Context, tenantID string, fn func(ctx context.Context, q storage.TenantScope) error) error {
	ctx, finish, err := st.db.BindTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	err = fn(ctx, st.db.Scoped(ctx))
	finish(err == nil)
	return err
}

// tenantPolicy — настройки тенанта для удалённого стола.
type tenantPolicy struct {
	Mode       string
	ProfileCap string
}

func (st store) policy(ctx context.Context, q storage.TenantScope, tenantID string) (tenantPolicy, error) {
	var p tenantPolicy
	err := q.QueryRow(ctx,
		`SELECT screen_access_mode, screen_profile_cap FROM tenants WHERE id = $1::uuid`, tenantID).
		Scan(&p.Mode, &p.ProfileCap)
	return p, err
}

// recordingUsed — сумма размеров записей тенанта (основа квоты, миграция 071).
func (st store) recordingUsed(ctx context.Context, q storage.TenantScope) (int64, error) {
	var n int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(recording_bytes), 0) FROM screen_sessions WHERE recording_path IS NOT NULL`).Scan(&n)
	return n, err
}

// deviceInfo — то, что нужно для решения «можно ли звать агента».
type deviceInfo struct {
	ID           string
	Status       string
	CN           string
	OS           string
	Capabilities []string
}

func (st store) device(ctx context.Context, q storage.TenantScope, deviceID string) (*deviceInfo, error) {
	var d deviceInfo
	err := q.QueryRow(ctx, `
		SELECT id::text, status, COALESCE(cert_cn, ''), COALESCE(os, ''), COALESCE(capabilities, '{}')
		FROM devices WHERE id = $1::uuid`, deviceID).
		Scan(&d.ID, &d.Status, &d.CN, &d.OS, &d.Capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

func (st store) get(ctx context.Context, q storage.TenantScope, id string) (*SessionRow, error) {
	s, err := scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+` FROM screen_sessions WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (st store) listByDevice(ctx context.Context, q storage.TenantScope, deviceID string, limit int) ([]*SessionRow, error) {
	rows, err := q.Query(ctx, `SELECT `+sessionCols+` FROM screen_sessions
		WHERE device_id = $1::uuid ORDER BY created_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionRow{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// liveOnDevice — незакрытые сеансы устройства.
func (st store) liveOnDevice(ctx context.Context, q storage.TenantScope, deviceID string) ([]*SessionRow, error) {
	rows, err := q.Query(ctx, `SELECT `+sessionCols+` FROM screen_sessions
		WHERE device_id = $1::uuid AND status IN ('requested', 'active')`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SessionRow
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type newSession struct {
	DeviceID      string
	OperatorID    string
	OperatorEmail string
	Reason        string
	Mode          string
	Profile       string
	Control       bool // сеанс с управлением: объявляется приглашением один раз
}

// create вставляет сеанс и задачу-приглашение. Задача сразу 'acked': доставляет её сам
// сервис, синхронно, а 'pending' подобрала бы общая очередь при переподключении агента и
// отвезла бы ему pb.Task БЕЗ screen_session — то есть пустой скрипт. Опоздавшее на часы
// приглашение к тому же уже никому не нужно.
func (st store) create(ctx context.Context, q storage.TenantScope, n newSession) (sessionID, taskID string, err error) {
	var opID *string
	if n.OperatorID != "" {
		opID = &n.OperatorID
	}
	err = q.QueryRow(ctx, `
		INSERT INTO screen_sessions (tenant_id, device_id, operator_id, operator_email, reason, mode, profile, control)
		SELECT d.tenant_id, d.id, $2::uuid, $3, $4, $5, $6, $7 FROM devices d WHERE d.id = $1::uuid
		RETURNING id::text`, n.DeviceID, opID, n.OperatorEmail, n.Reason, n.Mode, n.Profile, n.Control).Scan(&sessionID)
	if err != nil {
		return "", "", err
	}
	err = q.QueryRow(ctx, `
		INSERT INTO tasks (device_id, script_content, platform, priority, status, task_type, acked_at, tenant_id)
		SELECT d.id, '', COALESCE(d.os, 'unknown'), 'high', 'acked', 'screen_session', now(), d.tenant_id
		FROM devices d WHERE d.id = $1::uuid
		RETURNING id::text`, n.DeviceID).Scan(&taskID)
	if err != nil {
		return "", "", err
	}
	_, err = q.Exec(ctx, `UPDATE screen_sessions SET task_id = $2::uuid WHERE id = $1::uuid`, sessionID, taskID)
	return sessionID, taskID, err
}

// activate — первый hello агента. Только из 'requested': повторный hello того же сеанса
// (переподключение) не переоткрывает закрытый сеанс.
func (st store) activate(ctx context.Context, q storage.TenantScope, id string, w, h int, fallback bool) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE screen_sessions SET status = 'active', started_at = now(), last_viewer_at = now(),
			width = $2, height = $3, profile_fallback = $4
		WHERE id = $1::uuid AND status = 'requested'`, id, w, h, fallback)
	return tag.RowsAffected() == 1, err
}

// counters — монотонные счётчики сеанса. GREATEST: события едут durable-очередью и
// могут доехать после терминального события (миграции 071/074).
type counters struct {
	Frames, Bytes, Stalls, Dropped int64
}

func (st store) progress(ctx context.Context, q storage.TenantScope, id string, c counters) error {
	_, err := q.Exec(ctx, `
		UPDATE screen_sessions SET frames = GREATEST(frames, $2), bytes = GREATEST(bytes, $3),
			stalls = GREATEST(stalls, $4), dropped = GREATEST(dropped, $5)
		WHERE id = $1::uuid`, id, c.Frames, c.Bytes, c.Stalls, c.Dropped)
	return err
}

// telemetry — счётчики от агента с меткой времени: поздний повтор старого замера не
// применяется (миграция 072).
func (st store) telemetry(ctx context.Context, q storage.TenantScope, id, deviceID string, at time.Time, c counters) error {
	_, err := q.Exec(ctx, `
		UPDATE screen_sessions SET frames = GREATEST(frames, $3), bytes = GREATEST(bytes, $4),
			stalls = GREATEST(stalls, $5), dropped = GREATEST(dropped, $6), telemetry_at = $7
		WHERE id = $1::uuid AND device_id = $2::uuid AND (telemetry_at IS NULL OR telemetry_at < $7)`,
		id, deviceID, c.Frames, c.Bytes, c.Stalls, c.Dropped, at)
	return err
}

// end — идемпотентный перевод в 'ended'. Первая причина побеждает: поздний «AGENT_GONE»
// от обрыва стрима не должен затирать «OPERATOR_STOPPED», ради которого стрим и оборвали.
// deviceID пустой — закрывает сервер (без сверки устройства).
func (st store) end(ctx context.Context, q storage.TenantScope, id, deviceID, reason, detail string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE screen_sessions SET status = 'ended', ended_at = now(), end_reason = $3, end_detail = NULLIF($4, '')
		WHERE id = $1::uuid AND status <> 'ended' AND ($2 = '' OR device_id::text = $2)`,
		id, deviceID, reason, detail)
	return tag.RowsAffected() == 1, err
}

func (st store) setRecording(ctx context.Context, q storage.TenantScope, id, path string, n int64, truncated bool, why string) error {
	_, err := q.Exec(ctx, `
		UPDATE screen_sessions SET recording_path = NULLIF($2, ''), recording_bytes = GREATEST(recording_bytes, $3),
			recording_truncated = recording_truncated OR $4,
			recording_truncated_reason = CASE WHEN recording_truncated_reason = '' THEN $5 ELSE recording_truncated_reason END
		WHERE id = $1::uuid`, id, path, n, truncated, why)
	return err
}

func (st store) consent(ctx context.Context, q storage.TenantScope, id, deviceID, state string, at time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE screen_sessions SET consent_state = $3, consent_at = $4
		WHERE id = $1::uuid AND device_id = $2::uuid AND consent_state IS NULL`, id, deviceID, state, at)
	return err
}

func (st store) touchViewer(ctx context.Context, q storage.TenantScope, id string) error {
	_, err := q.Exec(ctx, `UPDATE screen_sessions SET last_viewer_at = now() WHERE id = $1::uuid AND status = 'active'`, id)
	return err
}

// expiredRecordings — записи тенанта старше срока хранения.
func (st store) expiredRecordings(ctx context.Context, q storage.TenantScope, retention time.Duration) (ids, paths []string, err error) {
	rows, err := q.Query(ctx, `SELECT id::text, recording_path FROM screen_sessions
		WHERE recording_path IS NOT NULL AND status = 'ended' AND ended_at < $1`, time.Now().Add(-retention))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, nil, err
		}
		ids, paths = append(ids, id), append(paths, p)
	}
	return ids, paths, rows.Err()
}

// recordingsOf — пути записей устройства (deviceID != "") или всего тенанта.
func (st store) recordingsOf(ctx context.Context, q storage.TenantScope, deviceID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT recording_path FROM screen_sessions
		WHERE recording_path IS NOT NULL AND ($1 = '' OR device_id::text = $1)`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// dropRecording — запись удалена с диска (срок хранения): строка остаётся, ссылка нет.
func (st store) dropRecording(ctx context.Context, q storage.TenantScope, id string) error {
	_, err := q.Exec(ctx, `UPDATE screen_sessions SET recording_path = NULL, recording_bytes = 0 WHERE id = $1::uuid`, id)
	return err
}

// deleteSession — строка завершённого сеанса; журнал ввода уходит каскадом.
func (st store) deleteSession(ctx context.Context, q storage.TenantScope, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM screen_sessions WHERE id = $1::uuid AND status = 'ended'`, id)
	return err
}

// Гранты на просмотр записей (миграция 067, users.can_view_session_recording).

type grant struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	CanView bool   `json:"can_view_session_recording"`
}

func (st store) grants(ctx context.Context, q storage.TenantScope) ([]grant, error) {
	rows, err := q.Query(ctx, `SELECT id::text, email, role, can_view_session_recording FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []grant{}
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.UserID, &g.Email, &g.Role, &g.CanView); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (st store) canView(ctx context.Context, q storage.TenantScope, userID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT can_view_session_recording FROM users WHERE id = $1::uuid`, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// setGrant выдаёт грант только it_admin (§5): viewer с правом на записи — это обход роли.
func (st store) setGrant(ctx context.Context, q storage.TenantScope, userID string, on bool) (*grant, error) {
	var g grant
	err := q.QueryRow(ctx, `
		UPDATE users SET can_view_session_recording = $2 AND role = 'it_admin'
		WHERE id = $1::uuid
		RETURNING id::text, email, role, can_view_session_recording`, userID, on).
		Scan(&g.UserID, &g.Email, &g.Role, &g.CanView)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

func (st store) setMode(ctx context.Context, q storage.TenantScope, tenantID, mode string) error {
	_, err := q.Exec(ctx, `UPDATE tenants SET screen_access_mode = $2 WHERE id = $1::uuid`, tenantID, mode)
	return err
}

func (st store) setProfileCap(ctx context.Context, q storage.TenantScope, tenantID, cap string) error {
	_, err := q.Exec(ctx, `UPDATE tenants SET screen_profile_cap = $2 WHERE id = $1::uuid`, tenantID, cap)
	return err
}

// returnControl — возврат управления сотруднику. Истина, если управление ещё было у
// оператора: повторный возврат ничего не меняет и не пишет второй строки журнала.
func (st store) returnControl(ctx context.Context, q storage.TenantScope, id string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE screen_sessions SET control_returned_at = now()
		WHERE id = $1::uuid AND control AND control_returned_at IS NULL AND status <> 'ended'`, id)
	return tag.RowsAffected() == 1, err
}

// inputEvent — строка журнала ввода (screen_input_events, миграция 073). detail пишется
// пустым для печатного ввода: что набрано, не хранится никогда (§9.21 п.6).
func (st store) inputEvent(ctx context.Context, q storage.TenantScope, sessionID, kind, detail string, events int, offsetMs int64) error {
	_, err := q.Exec(ctx, `
		INSERT INTO screen_input_events (tenant_id, device_id, session_id, operator_id, operator_email, kind, detail, events, offset_ms)
		SELECT tenant_id, device_id, id, operator_id, operator_email, $2, $3, $4, $5
		FROM screen_sessions WHERE id = $1::uuid`, sessionID, kind, detail, events, offsetMs)
	return err
}

// adminCount — сколько it_admin в тенанте (RLS ограничивает выборку текущим).
func (st store) adminCount(ctx context.Context, q storage.TenantScope) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'it_admin'`).Scan(&n)
	return n, err
}
