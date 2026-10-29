package security

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Process — запущенный процесс. Заполняется платформенным listProcesses
// (Windows: tasklist; macOS/BSD: ps; Linux: procfs — этот файл).
type Process struct {
	PID  int
	Name string // базовое имя исполняемого файла
	Cmd  string // путь/командное имя бинаря (без аргументов)
}

// procfsProcesses перечисляет процессы по procfs (Linux). `ps -axo comm=` тут
// не годится: ядро хранит comm в TASK_COMM_LEN=16, имя усекается до 15 символов,
// и forbidden-паттерн длиннее («gnome-calculator») НИКОГДА не совпадал — ПО
// запущено, а алерта нет. Полное имя берём из exe-симлинка/argv[0], усечённый
// comm — только последний фолбэк (kernel threads, недоступный cmdline).
//
// Парсинг отвязан от «/proc» (root параметром) и живёт в файле без build-тегов,
// чтобы регресс-тест на усечение гонялся на любой ОС.
func procfsProcesses(root string) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var ps []Process
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // не PID-каталог (sys, net, self, …)
		}
		name := procIdentity(filepath.Join(root, e.Name()))
		if name == "" {
			continue // процесс успел умереть между ReadDir и чтением
		}
		// Cmd = имя, БЕЗ пути: прежний ps comm= тоже отдавал только имя, а полный
		// путь в подстрочном матчинге findForbidden ловил бы паттерн в компонентах
		// каталогов (forbidden «telegram» алертил бы на /home/telegram-fan/bin/vim).
		ps = append(ps, Process{PID: pid, Name: name, Cmd: name})
	}
	return ps, nil
}

// procIdentity возвращает базовое имя исполняемого файла процесса (полное, не
// усечённое). Приоритет источников:
//   - /proc/<pid>/exe — истина от ядра, не усекается и не подделывается
//     процессом; читается root'ом (агент — служба), для чужих процессов без
//     прав отдаёт ошибку → идём ниже;
//   - argv[0] из /proc/<pid>/cmdline — не усекается, но процесс может его
//     переписать; берём только argv[0], НЕ всю командную строку — иначе паттерн
//     ловил бы имена в аргументах чужих программ (`vim chrome-notes.txt`);
//   - /proc/<pid>/comm — усечённый до 15 символов фолбэк: есть всегда, включая
//     kernel threads, у которых cmdline пуст.
func procIdentity(dir string) string {
	if target, err := os.Readlink(filepath.Join(dir, "exe")); err == nil && target != "" {
		// У перезаписанного на диске бинаря ядро дописывает " (deleted)".
		return filepath.Base(strings.TrimSuffix(target, " (deleted)"))
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		if argv0 := string(bytes.SplitN(raw, []byte{0}, 2)[0]); argv0 != "" {
			return filepath.Base(argv0)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		return strings.TrimSpace(string(raw))
	}
	return ""
}
