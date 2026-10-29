// Package service запускает агент как системную службу и ставит/снимает её.
//
// Без внешних зависимостей:
//   - macOS:   LaunchDaemon plist + launchctl (см. service_install_darwin.go)
//   - Windows: SCM + golang.org/x/sys/windows/svc (см. *_windows.go)
//   - прочее:  установка не поддерживается, Run работает в консольном режиме.
//
// Run(work) выполняет рабочую функцию под управлением менеджера служб (или
// напрямую в консоли) и блокирует до остановки. Install/Uninstall — управляют
// регистрацией службы и платформозависимы.
package service

import (
	"errors"
	"os"
)

// ErrServiceStartFailed — служба ЗАРЕГИСТРИРОВАНА, но немедленный старт не удался.
// Возвращается только Windows-реализацией Install (на macOS/Linux старт делает
// launchctl/systemctl и отдаёт свою ошибку). Вызывающий (runEnroll) трактует её как
// НЕ фатальную: продолжает Harden/трей/чистку легаси, потому что служба AUTO_START
// поднимется при следующем ребуте — пусть «один msiexec → служба уже работает» в
// этом редком случае и не выполнен. Так сбой старта не каскадит в пропуск хардненинга.
var ErrServiceStartFailed = errors.New("служба зарегистрирована, но не стартовала сразу")

// ErrTrayInstallFailed — LaunchDaemon (сама служба) установлен и запущен, но
// LaunchAgent трея поставить не удалось. Возвращается только macOS-реализацией
// Install. Вызывающий (runEnroll / подкоманда install) трактует её как НЕ фатальную:
// без меню-бара агент функционально полон (heartbeat, инвентарь, задачи, лок,
// self-update), а LaunchAgent из /Library/LaunchAgents launchd подхватит сам при
// следующем логоне. Раньше сбой трея делал return из Install ДО установки демона —
// и устройство оставалось вообще без агента, хотя демон поставить было можно
// (полевой баг: запись RoutineOps-agent.tray.plist упиралась в schg с прошлой установки).
// Сентинел — способ донести предупреждение наверх, не таща *slog.Logger в сигнатуру
// Install на всех платформах (тот же приём, что и с ErrServiceStartFailed).
var ErrTrayInstallFailed = errors.New("служба установлена, но трей (LaunchAgent) не поставился")

// Идентификатор и метаданные службы.
//
// Name — единое имя службы на всех платформах: SCM-имя в Windows
// (Get-Service RoutineOps-agent), Label LaunchDaemon в macOS
// (/Library/LaunchDaemons/RoutineOps-agent.plist) и основа имени systemd-юнита в Linux
// (RoutineOps-agent.service). Один идентификатор всюду — чтобы доки, установщик и
// админ-команды (Start-Service / systemctl / launchctl) ссылались на одно и то же.
const (
	Name        = "RoutineOps-agent"
	displayName = "RoutineOps Agent"
	description = "RoutineOps agent: mTLS gRPC heartbeat, inventory, task execution"
)

// Config — параметры установки службы.
type Config struct {
	// Args — аргументы подкоманды run, которые служба будет передавать бинарнику
	// (например -server, -cert, …). Должны быть с абсолютными путями к сертификатам.
	Args []string
	// Exe — абсолютный путь к бинарю в ExecStart/ProgramArguments. Пусто =
	// текущий исполняемый файл (os.Executable). На macOS/Linux после раскладки
	// (InstallLayout) сюда кладётся стабильный путь /usr/local/bin/RoutineOps-agent,
	// чтобы служба не указывала на скачанный во временный каталог бинарь.
	Exe string
	// WorkingDir — рабочий каталог службы (WorkingDirectory). Пусто = не задавать.
	// На macOS прописывается в plist; на Linux systemd-юнит и так фиксирует
	// WorkingDirectory=/var/lib/RoutineOps-agent.
	WorkingDir string
}

// exePath — путь к бинарю для ExecStart: явный cfg.Exe или текущий исполняемый файл.
func exePath(cfg Config) (string, error) {
	if cfg.Exe != "" {
		return cfg.Exe, nil
	}
	return os.Executable()
}
