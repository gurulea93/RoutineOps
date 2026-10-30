//go:build screen

package screen

import (
	"errors"
	"os"
	"path/filepath"
)

// webmRecording — запись сеанса WebRTC: .webm, который панель играет прямо в <video>.
// Формат и квота — как у Recorder (recorder.go); здесь только файл и имя в журнале.
type webmRecording struct {
	rec  *Recorder
	f    *os.File
	name string
}

func newWebmRecording(dir, sessionID string, quota int64) (*webmRecording, error) {
	if !uuidRe.MatchString(sessionID) {
		return nil, errors.New("screen: bad session id")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := sessionID + ".webm"
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &webmRecording{rec: NewRecorder(f, max(quota, 1)), f: f, name: name}, nil
}

// close завершает файл и возвращает то же, что rsfRecorder.close: имя, размер, причина
// усечения. Пустая запись (ни одного кадра) удаляется.
func (w *webmRecording) close() (name string, n int64, truncated string) {
	_ = w.rec.Close()
	n = w.rec.Bytes()
	if w.rec.Truncated() {
		truncated = truncTenantQuota
	}
	if n == 0 || w.rec.Frames() == 0 {
		_ = os.Remove(filepath.Join(filepath.Dir(w.f.Name()), w.name))
		return "", 0, truncated
	}
	return w.name, n, truncated
}
