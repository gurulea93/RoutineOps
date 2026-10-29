//go:build windows

package main

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"fyne.io/systray"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/Floodww/RoutineOps/internal/agent/applog"
	"github.com/Floodww/RoutineOps/internal/agent/service"
	"github.com/Floodww/RoutineOps/internal/agent/winsession"
)

//go:embed assets/tray.ico
var trayIcon []byte

// setTrayIcon: область уведомлений Windows берёт многоразмерный .ico и сама
// выбирает нужный размер под текущий DPI (см. brand/tray/windows/tray.ico — 16/24/32).
func setTrayIcon() { systray.SetIcon(trayIcon) }

// launchTrayInActiveSession поднимает трей в активной консольной сессии сразу после
// установки. Вызывается из enroll -install-service, который MSI исполняет под
// LocalSystem (session 0, без рабочего стола): нарисовать трей оттуда нельзя, но как
// SYSTEM мы стартуем "<exe> tray" в сессии залогиненного пользователя
// (CreateProcessAsUser — тот же механизм, что поднимает лок-оверлей). Иначе трей
// появился бы лишь при следующем логоне (HKLM\…\Run). Best-effort: нет активной
// сессии (никто не залогинен) — норма, трей поднимется при логоне через Run-ключ.
func launchTrayInActiveSession(log *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		log.Warn("трей: не удалось определить путь бинаря для запуска в сессии", slog.Any("error", err))
		return
	}
	proc, err := winsession.LaunchInActiveSession(exe, []string{"tray"})
	if err != nil {
		if !errors.Is(err, winsession.ErrNoActiveSession) {
			log.Warn("трей: не удалось запустить в активной сессии", slog.Any("error", err))
		}
		return
	}
	windows.CloseHandle(proc) // запускаем и забываем — трей живёт до логоффа
	log.Info("трей запущен в активной сессии сразу после установки")
}

// relaunchTrayAtServiceStart поднимает трей при старте демона под SCM. Нужно после
// самообновления: замена exe убивает трей юзер-сессии taskkill'ом (он держит
// блокировку .old), служба перезапускается через os.Exit(1) → failure-actions, и
// без этого вызова иконка не возвращалась до перелогина (полевой баг self-update
// 2.3.0→2.4.1: Run-ключ трея срабатывает только на логоне).
//
// От дубля при живом трее (штатный Restart-Service; enroll-путь, где службу стартует
// service.Install, а следом launchTrayInActiveSession зовёт сам enroll) защищает
// сессионный мьютекс в trayAlreadyRunning: сама systray второй ПРОЦЕСС не отвергает —
// пространство имён оконных классов в Win32 процессное, и без гарда появлялась бы
// вторая иконка. Нет активной сессии (старт при загрузке, до логона) — no-op внутри
// launchTrayInActiveSession, трей поднимет Run-ключ на логоне.
//
// Вне SCM (отладочный `agent run` в консоли) не лезем: WTSQueryUserToken требует
// SYSTEM, и попытка давала бы только Warn-шум при каждом ручном запуске.
func relaunchTrayAtServiceStart(log *slog.Logger) {
	if isSvc, err := svc.IsWindowsService(); err != nil || !isSvc {
		return
	}
	launchTrayInActiveSession(log)
}

// trayAlreadyRunning отвечает, висит ли в ЭТОЙ логон-сессии другой процесс трея.
// Гард обязателен: systray НЕ падает во второй инстанции (RegisterClassEx успешен —
// пространство имён оконных классов процессное), а Shell_NotifyIcon без NIF_GUID
// вешает отдельную вторую иконку с новым hwnd. Источников дубля три: Run-ключ на
// логоне, relaunchTrayAtServiceStart при каждом старте службы и
// launchTrayInActiveSession из enroll. Тот же паттерн, что singleInstance() в lockui;
// Local\ — у каждой логон-сессии своё пространство имён, так что при быстром
// переключении пользователей каждый держит свой трей. Хэндл намеренно не закрываем —
// мьютекс живёт до конца процесса.
func trayAlreadyRunning() bool {
	name, err := windows.UTF16PtrFromString(`Local\RoutineOpsAgentTray`)
	if err != nil {
		return false // не смогли проверить — показываем: лишняя иконка лучше отсутствующей
	}
	_, err = windows.CreateMutex(nil, false, name)
	return err == windows.ERROR_ALREADY_EXISTS
}

// serviceRegistered отвечает, зарегистрирована ли служба агента в SCM.
//
// Открываем SCM с минимальным правом SC_MANAGER_CONNECT: трей живёт в сессии
// ОБЫЧНОГО пользователя, а mgr.Connect() из x/sys просит SC_MANAGER_ALL_ACCESS и у
// него упал бы с «отказано в доступе» — то есть на непривилегированной машине мы бы
// решили, что службы нет, и спрятали рабочий трей.
//
// Fail-open: «не установлена» возвращаем ТОЛЬКО по явному
// ERROR_SERVICE_DOES_NOT_EXIST. Любая другая ошибка (нет прав спросить, SCM недоступен)
// — показываем иконку: прятать индикатор рабочего агента хуже, чем показать лишний.
func serviceRegistered() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return true
	}
	defer windows.CloseServiceHandle(scm)

	name, err := windows.UTF16PtrFromString(service.Name)
	if err != nil {
		return true
	}
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
	}
	windows.CloseServiceHandle(h)
	return true
}

func setupTrayPlatform() {
	// Второй трей в этой же сессии уже висит — выходим молча ДО systray.Run: иначе
	// появилась бы вторая иконка (см. trayAlreadyRunning), а каждая инстанция ещё и
	// независимо следит за lock.json и плодит процессы lock-screen.
	if trayAlreadyRunning() {
		os.Exit(0)
	}

	// Иконка в трее — индикатор АГЕНТА, и появляться она должна только когда агент
	// есть. MSI же кладёт Run-ключ MDMTray безусловно, вместе с файлами, а служба
	// возникает лишь если enroll УСПЕЛ (EnrollExec стоит Return="ignore"). Поле,
	// 13.07: enroll упал на TLS → службы нет, но на логоне Run-ключ поднимал трей, и
	// установка выглядела рабочей. Нет службы — нет иконки: пусть отсутствие агента
	// будет видно, а не замаскировано.
	//
	// Проверяем ДО ShowWindow ниже: бинарь собран GUI-subsystem (-H windowsgui), своей
	// консоли у него нет, и attachParentConsole() в main цепляет консоль РОДИТЕЛЯ —
	// значит прятать нам было бы нечего (запуск из Run-ключа), либо мы спрятали бы
	// PowerShell того, кто вручную запустил трей для проверки.
	if !serviceRegistered() {
		// В stderr писать бессмысленно (под Run-ключом его никто не увидит), поэтому —
		// в тот же файл, куда пишут служба и enroll: там это и будут искать. Запись
		// небуферизованная, поэтому os.Exit её не съест (closer не успел бы отработать).
		log, _, _ := applog.NewServiceLogger(service.LogFilePath(), slog.LevelInfo)
		log.Warn("трей не запущен: служба не зарегистрирована — агент не энроллен",
			slog.String("service", service.Name))
		// Выходим, а не return: вызывающий runTray сразу поднял бы systray с иконкой.
		os.Exit(0)
	}

	// Агент — консольное приложение (CLI-подкоманды печатают в stdout), поэтому при
	// автозапуске трея из Run-ключа всплывает пустое окно консоли. Прячем его —
	// трей должен жить незаметно в фоне (только иконка в области уведомлений).
	if hwnd := win.GetConsoleWindow(); hwnd != 0 {
		win.ShowWindow(hwnd, win.SW_HIDE)
	}
}

func configureLockScreenCmd(cmd *exec.Cmd) {
	// Без HideWindow на долю секунды мелькает чёрное окно консоли дочернего
	// процесса до отрисовки оверлея (агент — консольное приложение).
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
