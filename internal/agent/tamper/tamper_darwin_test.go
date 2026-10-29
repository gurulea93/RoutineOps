//go:build darwin

package tamper

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUnlockClearsSchg воспроизводит сам механизм бага: по immutable-файлу O_TRUNC
// отвергается даже у root, и это ровно то, что убивало повторную установку. Требует
// root (chflags schg — привилегированная операция), иначе пропускается.
func TestUnlockClearsSchg(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chflags schg требует root")
	}
	p := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(p, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("chflags", "schg", p).CombinedOutput(); err != nil {
		t.Fatalf("chflags schg: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "noschg", p).Run() }) // иначе t.TempDir() не подчистится
	if f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		f.Close()
		t.Fatal("O_TRUNC по schg-файлу прошёл — тест не воспроизводит защиту")
	}
	if err := Unlock(p); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("после Unlock запись всё ещё запрещена: %v", err)
	}
	f.Close()
}

// TestArmWithoutRootReports — Arm без root обязан ВЕРНУТЬ ошибку, а не молча отдать nil.
// Живой баг: установка без root ставила агента вообще без schg, при этом Arm рапортовал
// успехом и log.Warn в main.go не срабатывал — «защита взведена» была ложью в логе.
// Тест намеренно гоняется ИМЕННО от не-root (обратный скип к остальным тестам файла):
// граница, на которой ломалось, — это euid != 0.
func TestArmWithoutRootReports(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("тест проверяет путь без root")
	}
	err := Arm(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("Arm без root вернул nil — агент остаётся незащищённым, а лог рапортует успех")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("ожидали os.ErrPermission, получили: %v", err)
	}
}
