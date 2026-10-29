package applog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenCreatesDirAndAppends: Open создаёт отсутствующий каталог и дозаписывает.
func TestOpenCreatesDirAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent.log")
	w, err := Open(path, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Повторный Open на дозапись не затирает прошлое содержимое.
	w2, err := Open(path, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write #2: %v", err)
	}
	_ = w2.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(b); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("ожидали дозапись обеих строк, получили: %q", got)
	}
}

// TestRotation: превышение maxBytes сдвигает текущий файл в .1, новый стартует с нуля.
func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	w, err := Open(path, 16) // крошечный порог — ротация почти сразу
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("AAAAAAAAAA\n")); err != nil { // 11 байт
		t.Fatalf("Write 1: %v", err)
	}
	if _, err := w.Write([]byte("BBBBBBBBBB\n")); err != nil { // +11 > 16 → ротация перед записью
		t.Fatalf("Write 2: %v", err)
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("бэкап .1 не создан: %v", err)
	}
	if !strings.Contains(string(backup), "AAAA") {
		t.Errorf("в бэкапе ожидали первую запись, получили: %q", backup)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile текущего: %v", err)
	}
	if !strings.Contains(string(cur), "BBBB") || strings.Contains(string(cur), "AAAA") {
		t.Errorf("текущий файл должен содержать только вторую запись, получили: %q", cur)
	}
}

// TestNewServiceLogger: логгер пишет в файл; при недоступном пути не падает, отдаёт
// stderr-only логгер и ошибку.
func TestNewServiceLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	lg, closer, err := NewServiceLogger(path, slog.LevelInfo)
	if err != nil {
		t.Fatalf("NewServiceLogger: %v", err)
	}
	lg.Info("проверка", "k", "v")
	_ = closer.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "проверка") {
		t.Errorf("лог не попал в файл: %q", b)
	}

	// Путь-каталог вместо файла → открыть нельзя, но логгер всё равно валиден.
	lg2, closer2, err := NewServiceLogger(t.TempDir(), slog.LevelInfo)
	if err == nil {
		t.Error("ожидали ошибку открытия файла по пути-каталогу")
	}
	if lg2 == nil || closer2 == nil {
		t.Fatal("при ошибке должны вернуться валидный stderr-логгер и no-op closer")
	}
	lg2.Info("в stderr") // не должно паниковать
	_ = closer2.Close()
}
