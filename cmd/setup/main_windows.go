//go:build windows

// RoutineOps-setup.exe — установщик агента одним файлом. Сам по себе ничего не знает: MSI и
// параметры энроллмента приклеены к хвосту файла панелью (internal/setupbundle). Запускается
// с правами администратора (манифест), распаковывает MSI во временный каталог и ставит его
// через msiexec с уже заданными свойствами.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/Floodww/RoutineOps/internal/setupbundle"
)

const title = "RoutineOps — установка агента"

func main() {
	os.Exit(run())
}

func run() int {
	exe, err := os.Executable()
	if err != nil {
		return fail("Не удалось определить путь установщика: " + err.Error())
	}
	f, err := os.Open(exe)
	if err != nil {
		return fail("Не удалось открыть установщик: " + err.Error())
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fail(err.Error())
	}
	params, msi, err := setupbundle.Read(f, st.Size())
	f.Close()
	if err != nil {
		return fail("В этом файле нет данных установки. Скачайте установщик заново из панели RoutineOps.\n\n" + err.Error())
	}

	dir, err := os.MkdirTemp("", "routineops-setup-")
	if err != nil {
		return fail("Не удалось создать временный каталог: " + err.Error())
	}
	defer os.RemoveAll(dir)
	msiPath := filepath.Join(dir, "RoutineOps-agent.msi")
	if err := os.WriteFile(msiPath, msi, 0o600); err != nil {
		return fail("Не удалось распаковать MSI: " + err.Error())
	}
	logPath := filepath.Join(os.TempDir(), "RoutineOps-setup.log")

	cmd := exec.Command("msiexec.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "msiexec.exe " + params.MSIArgs(msiPath, logPath)}
	err = cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		return fail("Не удалось запустить msiexec: " + err.Error())
	}
	switch code {
	case 0, 3010: // 3010 — нужна перезагрузка, установка при этом успешна
		note("Агент RoutineOps установлен и подключается к серверу.")
		return 0
	case 1603:
		return fail(fmt.Sprintf("Установка не удалась (код 1603). Вероятно, агент уже установлен: "+
			"удалите его в «Приложениях» и запустите установщик снова.\n\nЖурнал: %s", logPath))
	default:
		return fail(fmt.Sprintf("Установка не удалась (код %d).\n\nЖурнал: %s", code, logPath))
	}
}

func note(text string) {
	_, _ = windows.MessageBox(0, windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(title),
		windows.MB_OK|windows.MB_ICONINFORMATION)
}

func fail(text string) int {
	_, _ = windows.MessageBox(0, windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(title),
		windows.MB_OK|windows.MB_ICONERROR)
	return 1
}
