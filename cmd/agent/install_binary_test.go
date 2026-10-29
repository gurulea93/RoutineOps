package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestInstallBinaryReplacesBusyBinary: повторная установка кладёт бинарь поверх того,
// который прямо сейчас исполняется демоном (а на macOS ещё и защищён chflags schg).
// Перезапись НА МЕСТЕ там запрещена ядром (ETXTBSY / immutable), поэтому installBinary
// обязан подменять файл через temp+rename. Проверяем именно это свойство: уже открытый
// дескриптор должен продолжать видеть СТАРОЕ содержимое (старый inode пережил подмену) —
// ровно так живой процесс доживает со своим бинарём.
func TestInstallBinaryReplacesBusyBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows раскладка не выполняется: InstallLayout().Relocate=false (MSI)")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "new-agent")
	dst := filepath.Join(dir, "RoutineOps-agent")
	if err := os.WriteFile(src, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	busy, err := os.Open(dst) // «запущенный» агент держит старый inode
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	if err := installBinary(src, dst); err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Fatalf("по пути службы %q, хотим \"NEW\"", got)
	}
	buf := make([]byte, 3)
	if _, err := busy.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "OLD" {
		t.Fatalf("старый дескриптор видит %q — файл перезаписан на месте, а не подменён rename", buf)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("права нового бинаря %v, хотим 0755", fi.Mode().Perm())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 { // src + dst, temp-файла остаться не должно
		t.Fatalf("в каталоге %d файлов, хотим 2: %v", len(ents), ents)
	}
}

// TestInstallBinarySelfIsNoop: postinstall pkg запускает УЖЕ установленный
// /usr/local/bin/RoutineOps-agent — src==dst, подменять нечего.
func TestInstallBinarySelfIsNoop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "RoutineOps-agent")
	if err := os.WriteFile(p, []byte("SELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installBinary(p, p); err != nil {
		t.Fatalf("installBinary(src==dst): %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "SELF" {
		t.Fatalf("файл изменился: %q", b)
	}
}
