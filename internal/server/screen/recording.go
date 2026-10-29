//go:build screen

package screen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Запись сеанса RSF1 — тот же поток записей [u32 len][u32 rel_ms][payload], что отдаёт
// /live: проигрыватель панели разбирает её тем же parseRecords, и второго формата нет.
//
// Путь в БД — БЕЗ каталога и без tenant_id (§5): тенант живёт в строке сеанса, иначе
// перенос устройства между тенантами потребовал бы двигать байты по диску.

const (
	// DefaultMaxSessionBytes — потолок записи одного сеанса.
	DefaultMaxSessionBytes int64 = 512 << 20
	// DefaultTenantQuota — потолок суммы записей тенанта (миграция 071).
	DefaultTenantQuota int64 = 20 << 30
)

// Причины неполной записи (web SCREEN_TRUNCATION).
const (
	truncQuota       = "quota"
	truncTenantQuota = "tenant_quota"
	truncIOError     = "io_error"
)

var errRecordingStopped = errors.New("screen: recording stopped")

// rsfRecorder пишет записи до потолка. Потолок — min(сеанс, остаток квоты тенанта на
// момент начала), поэтому при упоре известно, чей именно потолок кончился.
type rsfRecorder struct {
	mu        sync.Mutex
	f         *os.File
	name      string
	n         int64
	limit     int64
	tenantCap bool // limit задан остатком квоты тенанта, а не потолком сеанса
	started   bool // первый ключевой кадр уже записан
	truncated string
}

// recordingName — имя файла записи. id — UUID из БД; проверка — чтобы путь никогда не
// вышел за каталог записей, даже если в строку попало что-то чужое.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func recordingName(sessionID string) (string, error) {
	if !uuidRe.MatchString(sessionID) {
		return "", fmt.Errorf("screen: bad session id %q", sessionID)
	}
	return sessionID + ".rsf", nil
}

// recordingPath — абсолютный путь по имени из БД. Имя с разделителями отвергается.
func recordingPath(dir, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("screen: bad recording name %q", name)
	}
	return filepath.Join(dir, name), nil
}

func newRSFRecorder(dir, sessionID string, sessionMax, tenantLeft int64) (*rsfRecorder, error) {
	name, err := recordingName(sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	r := &rsfRecorder{f: f, name: name, limit: sessionMax}
	if tenantLeft < sessionMax {
		r.limit, r.tenantCap = max(tenantLeft, 0), true
	}
	return r, nil
}

// write дописывает запись. Ошибка — запись остановлена (причина в truncated): сеанс
// после этого закрывается, смотреть без записи нельзя (§6).
func (r *rsfRecorder) write(p *screenframe.Part, rec []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.truncated != "" || r.f == nil {
		return errRecordingStopped
	}
	if !r.started {
		if !p.Keyframe {
			return nil // до первого ключевого кадра проигрывать нечего
		}
		r.started = true
	}
	if r.n+int64(len(rec)) > r.limit {
		r.truncated = truncQuota
		if r.tenantCap {
			r.truncated = truncTenantQuota
		}
		return errRecordingStopped
	}
	if _, err := r.f.Write(rec); err != nil {
		r.truncated = truncIOError
		return errRecordingStopped
	}
	r.n += int64(len(rec))
	return nil
}

// close закрывает файл. Пустая запись удаляется: ссылка на файл без кадров только
// создаёт видимость улики.
func (r *rsfRecorder) close() (name string, n int64, truncated string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return r.name, r.n, r.truncated
	}
	path := r.f.Name()
	if err := r.f.Sync(); err != nil && r.truncated == "" {
		r.truncated = truncIOError
	}
	if err := r.f.Close(); err != nil && r.truncated == "" {
		r.truncated = truncIOError
	}
	r.f = nil
	if r.n == 0 {
		_ = os.Remove(path)
		r.name = ""
	}
	return r.name, r.n, r.truncated
}

// endReason — код завершения сеанса по причине остановки записи.
func truncationEndReason(t string) string {
	if t == truncIOError {
		return "RECORDING_FAILED"
	}
	return "RECORDING_QUOTA"
}

// appendRecord собирает запись потока для хаба и файла.
func appendRecord(relMs uint32, payload []byte) []byte {
	return screenframe.AppendRecord(make([]byte, 0, screenframe.RecordHeaderLen+len(payload)), relMs, payload)
}
