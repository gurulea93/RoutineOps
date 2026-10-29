// Package applog даёт файловый лог-синк для агента под службой. Служба пишет
// stderr в никуда (Windows) или в journald (Linux/systemd) — но для полевой
// диагностики старта и подключения нужен отдельный файл, который можно забрать с
// машины. Синк потокобезопасен и ограничен по размеру (один бэкап), чтобы лог не
// рос безгранично на долгоживущей службе.
package applog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// DefaultMaxBytes — порог ротации файла лога службы (8 МБ + один бэкап .1).
const DefaultMaxBytes = 8 << 20

// rotating — io.WriteCloser в файл с ротацией по размеру: при превышении maxBytes
// текущий файл переименовывается в <path>.1 (затирая прошлый бэкап), запись
// продолжается в новый. Логирование НИКОГДА не должно ронять агента, поэтому при
// сбое файловых операций Write молча «успешен» (данные теряются, но не падаем).
type rotating struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

// Open создаёт каталог и открывает файл лога на дозапись.
func Open(path string, maxBytes int64) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	var sz int64
	if fi, serr := f.Stat(); serr == nil {
		sz = fi.Size()
	}
	return &rotating{path: path, maxBytes: maxBytes, f: f, size: sz}, nil
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBytes > 0 && r.size+int64(len(p)) > r.maxBytes {
		r.rotate()
	}
	if r.f == nil {
		return len(p), nil // файл недоступен (диск полон и т.п.) — не валим агента
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate закрывает текущий файл, сдвигает его в .1 и открывает новый. При неудаче
// пытается снова открыть прежний на дозапись, чтобы не потерять последующие записи.
func (r *rotating) rotate() {
	_ = r.f.Close()
	_ = os.Rename(r.path, r.path+".1")
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.f, _ = os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		return
	}
	r.f = f
	r.size = 0
}

func (r *rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// tolerantWriter оборачивает writer так, что его ошибки НЕ прерывают MultiWriter.
// Нужно для os.Stderr: под Windows-службой это невалидный хэндл, и без обёртки
// io.MultiWriter падал бы на первом же writer'е и НЕ доходил до файла — из-за чего
// файловый лог службы был пуст всегда (диагностировано по 1053).
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p) // ошибку глушим: невалидный stderr не должен глушить файл
	return len(p), nil
}

// NewServiceLogger строит логгер службы, который пишет И в stderr (виден в консоли
// и в journald под systemd), И в файл path. Если файл открыть нельзя (нет прав —
// напр. консольный запуск под обычным пользователем), возвращает stderr-only
// логгер и ошибку: агент не должен падать или молчать из-за недоступного лога.
func NewServiceLogger(path string, level slog.Level) (*slog.Logger, io.Closer, error) {
	opts := &slog.HandlerOptions{Level: level}
	w, err := Open(path, DefaultMaxBytes)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), noopCloser{}, err
	}
	// stderr заворачиваем в tolerantWriter: под службой он мёртв, но не должен
	// мешать записи в файл (см. tolerantWriter).
	mw := io.MultiWriter(tolerantWriter{os.Stderr}, w)
	return slog.New(slog.NewTextHandler(mw, opts)), w, nil
}
