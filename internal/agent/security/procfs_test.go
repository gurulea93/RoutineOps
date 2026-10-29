package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeProc раскладывает фейковый /proc/<pid> с заданными comm и cmdline
// (cmdline — argv, склеенные NUL, как в настоящем procfs).
func writeProc(t *testing.T, root, pid, comm string, argv ...string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if comm != "" {
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(argv) > 0 {
		var raw []byte
		for _, a := range argv {
			raw = append(raw, a...)
			raw = append(raw, 0)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Регресс БАГА «ps comm= усекает имя до 15 символов»: ядро хранит comm в
// TASK_COMM_LEN=16, и forbidden-паттерн "gnome-calculator" (16 символов) никогда
// не совпадал с усечённым "gnome-calculato". procfs-листинг обязан вернуть
// ПОЛНОЕ имя из argv[0], а не comm.
func TestProcfs_LongNameNotTruncated(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "101", "gnome-calculato", "/usr/bin/gnome-calculator", "--standalone")

	ps, err := procfsProcesses(root)
	if err != nil {
		t.Fatalf("procfsProcesses: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("процессов = %d, хотим 1 (%v)", len(ps), ps)
	}
	if ps[0].Name != "gnome-calculator" || ps[0].PID != 101 {
		t.Fatalf("процесс = %+v, хотим Name=gnome-calculator PID=101", ps[0])
	}
	// Сквозная проверка: паттерн длиннее 15 символов теперь находится.
	found := findForbidden(ps, []string{"gnome-calculator"})
	if _, ok := found["gnome-calculator"]; !ok {
		t.Fatal("findForbidden не нашёл длинное имя — усечение не побеждено")
	}
}

// exe-симлинк приоритетнее argv[0] (истина от ядра; процесс может переписать
// argv). Суффикс « (deleted)» перезаписанного бинаря срезается.
func TestProcfs_ExeSymlinkPreferred(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink на Windows требует привилегий")
	}
	root := t.TempDir()
	writeProc(t, root, "202", "fake-name", "/usr/bin/fake-from-argv")
	// Симлинк может быть «висячим» — Readlink читает цель без разыменования.
	if err := os.Symlink("/opt/real/teamviewer-daemon (deleted)", filepath.Join(root, "202", "exe")); err != nil {
		t.Fatal(err)
	}

	ps, err := procfsProcesses(root)
	if err != nil {
		t.Fatalf("procfsProcesses: %v", err)
	}
	if len(ps) != 1 || ps[0].Name != "teamviewer-daemon" {
		t.Fatalf("процесс = %+v, хотим Name=teamviewer-daemon (из exe, без « (deleted)»)", ps)
	}
}

// Kernel thread: cmdline пуст, есть только comm → берём comm (усечение тут
// неизбежно и безвредно — kernel thread не «запрещённое ПО»).
func TestProcfs_KernelThreadFallsBackToComm(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "303", "kworker/0:1H" /* без cmdline */)

	ps, err := procfsProcesses(root)
	if err != nil {
		t.Fatalf("procfsProcesses: %v", err)
	}
	if len(ps) != 1 || ps[0].Name != "kworker/0:1H" {
		t.Fatalf("процесс = %+v, хотим Name=kworker/0:1H", ps)
	}
}

// Не-PID записи procfs (sys, net, self, файлы) пропускаются молча.
func TestProcfs_SkipsNonPIDEntries(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "404", "bash", "/bin/bash")
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("1 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PID-каталог без comm/cmdline/exe — процесс умер между ReadDir и чтением.
	if err := os.MkdirAll(filepath.Join(root, "505"), 0o755); err != nil {
		t.Fatal(err)
	}

	ps, err := procfsProcesses(root)
	if err != nil {
		t.Fatalf("procfsProcesses: %v", err)
	}
	if len(ps) != 1 || ps[0].PID != 404 {
		t.Fatalf("процессы = %+v, хотим ровно PID=404", ps)
	}
}
