package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildProbe собирает крошечный бинарь с заданным CGO_ENABLED и возвращает путь к нему.
// Собираем НАСТОЯЩИЙ бинарь (не подсовываем фикстуру): guard читает Go-buildinfo, и
// проверять его надо ровно через ту же границу — реальный тулчейн, реальные Settings.
// GOOS не переопределяем: buildinfo.CGO_ENABLED пишется одинаково на всех платформах,
// поэтому тест одинаково валиден и на маке (где собирают релиз), и на linux-CI.
func buildProbe(t *testing.T, cgo string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"),
		[]byte("module probe\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(src, "probe.bin")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED="+cgo)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("сборка пробника с CGO_ENABLED=%s: %v\n%s", cgo, err, b)
	}
	return out
}

// TestRequireCGODarwin_RejectsNonCGO — сердце находки: бинарь, собранный с CGO_ENABLED=0,
// внешне валиден (компилируется, sha256 считается, подпись ложится), и раньше уезжал в
// парк без Cocoa-замка и Keychain. Guard обязан его отвергнуть.
func TestRequireCGODarwin_RejectsNonCGO(t *testing.T) {
	err := requireCGODarwin(buildProbe(t, "0"))
	if err == nil {
		t.Fatal("не-cgo бинарь принят к публикации — guard не работает")
	}
	if !errors.Is(err, errNotCGO) {
		t.Fatalf("ожидали errNotCGO, получили: %v", err)
	}
}

// TestRequireCGODarwin_AcceptsCGO — обратная сторона: штатная сборка (make build-mac-native)
// проходит. Без этого guard мог бы «работать», отвергая вообще всё.
func TestRequireCGODarwin_AcceptsCGO(t *testing.T) {
	if err := requireCGODarwin(buildProbe(t, "1")); err != nil {
		t.Fatalf("cgo-бинарь отвергнут: %v", err)
	}
}

// TestRequireCGODarwin_RejectsGarbage — не-Go файл (обрезанный/чужой бинарь) не должен
// молча проезжать: buildinfo не читается → публикацию останавливаем.
func TestRequireCGODarwin_RejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "garbage")
	if err := os.WriteFile(p, []byte("not a go binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := requireCGODarwin(p); err == nil {
		t.Fatal("мусорный файл принят к публикации")
	}
}
