//go:build darwin

package tamper

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// На macOS Tamper Protection реализована через системный флаг schg (system immutable).
// Флаг предотвращает удаление/изменение файлов даже пользователем root (пока флаг не будет снят).

func targets() []string {
	return []string{
		"/usr/local/bin/RoutineOps-agent",
		"/Library/LaunchDaemons/RoutineOps-agent.plist",
		"/Library/LaunchAgents/RoutineOps-agent.tray.plist",
	}
}

// Arm взводит защиту: накладывает schg на бинарный файл и Launch-файлы.
//
// Возвращает ошибку, если защиту взвести не удалось (нет root, отказал chflags). Раньше
// оба случая молча возвращали nil и печатали «файлы защищены» — агент оставался
// незащищённым, а лог это скрывал. Вызывающая сторона на ошибку лишь предупреждает и
// установку не валит (cmd/agent/main.go: tamper.Arm → log.Warn), так что честный отказ
// ничего не ломает, но перестаёт врать.
func Arm(log *slog.Logger) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("chflags schg требует root: %w", os.ErrPermission)
	}

	var failed []string
	for _, t := range targets() {
		if _, err := os.Stat(t); err != nil {
			continue // цели может не быть (трей не ставился) — это не отказ защиты
		}
		if out, err := exec.Command("chflags", "schg", t).CombinedOutput(); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v: %s)", t, err, strings.TrimSpace(string(out))))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("chflags schg не наложен на: %s", strings.Join(failed, "; "))
	}
	log.Info("tamper-protection (macOS): файлы защищены флагом schg")
	return nil
}

// Disarm снимает флаг schg.
func Disarm() error {
	if os.Geteuid() != 0 {
		return os.ErrPermission
	}

	for _, t := range targets() {
		if _, err := os.Stat(t); err == nil {
			_ = exec.Command("chflags", "noschg", t).Run()
		}
	}
	return nil
}

// immutableFlags — биты неизменяемости из stat(2): SF_IMMUTABLE (schg — его вешает
// Arm) и UF_IMMUTABLE (uchg — мог повесить админ вручную). Любой из них заставляет
// ядро отвергать write/O_TRUNC/rename/unlink ДАЖЕ у root, пока флаг не снят.
const immutableFlags = 0x00020000 | 0x00000002 // SF_IMMUTABLE | UF_IMMUTABLE

// isImmutable — стоит ли на файле immutable-флаг. Читаем stat, а не дёргаем chflags
// вслепую: на чистой машине защищаемых файлов ещё нет, и лишний вызов внешней
// утилиты только маскировал бы настоящие ошибки (и шумел бы в первой установке).
func isImmutable(path string) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false // файла нет или он недоступен — снимать нечего
	}
	return st.Flags&immutableFlags != 0
}

// Unlock снимает immutable-флаг с указанных путей (пустой список = все targets()).
// Это НЕ пользовательское разоружение (для него есть Disarm), а примитив
// install-пути: Arm держит бинарь и оба plist под schg, а ядро по такому файлу
// отвергает O_TRUNC/rename/unlink даже у root — без снятия флага агент блокирует
// сам себя при ЛЮБОЙ повторной установке (переустановка pkg, апгрейд, повторный
// enroll) и при собственном uninstall.
//
// Защиту это не ослабляет: schg на macOS (securelevel=0) и так снимается любым
// root-процессом, а все вызывающие — install/uninstall, которым root нужен и без
// того. Обратно защиту взводит Arm в конце установки (runEnroll/`install`).
func Unlock(paths ...string) error {
	if len(paths) == 0 {
		paths = targets()
	}
	for _, p := range paths {
		if p == "" || !isImmutable(p) {
			continue
		}
		// Раньше сюда прилетал безликий EPERM из os.OpenFile («нужен root?»), хотя root
		// как раз был: настоящая причина — флаг. Говорим прямо.
		if os.Geteuid() != 0 {
			return fmt.Errorf("%s под tamper-защитой (schg): снять флаг может только root", p)
		}
		// nouchg заодно: uchg отвергает запись так же, как schg, и мог остаться от
		// ручной защиты админа — иначе установка падала бы с тем же невнятным EPERM.
		if out, err := exec.Command("chflags", "noschg,nouchg", p).CombinedOutput(); err != nil {
			return fmt.Errorf("chflags noschg %s: %w (%s)", p, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Cleanup — no-op на macOS, т.к. Disarm уже снимает флаги.
func Cleanup() {}

// Enforce — no-op, так как schg обеспечивается ядром macOS.
func Enforce(context.Context, *slog.Logger) {}

// Status возвращает статус защиты (1 если защищен, 0 если нет).
func Status() (protection, safeBoot uint32, safeMode bool) {
	outFlags, err := exec.Command("ls", "-lO", "/usr/local/bin/RoutineOps-agent").Output()
	if err == nil {
		s := string(outFlags)
		if strings.Contains(s, "schg") {
			return 1, 0, false
		}
	}
	return 0, 0, false
}

// SafeMode на macOS пока считаем false.
func SafeMode() bool {
	return false
}
