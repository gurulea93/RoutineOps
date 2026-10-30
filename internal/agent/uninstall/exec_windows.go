//go:build windows

package uninstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/Floodww/RoutineOps/internal/agent/collector"
	"github.com/Floodww/RoutineOps/internal/agent/silentuninstall"
	"github.com/Floodww/RoutineOps/internal/agent/winsession"
)

// Ветки ARP, из которых коллектор читает установленное ПО. Ищем строку снятия
// РОВНО там же и по тому же ключу (Software.UninstallID — имя подключа).
const (
	uninstallKeyPath     = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	uninstallKeyPathWow  = `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`
	msiexecName          = "msiexec.exe"
	quietUninstallString = "QuietUninstallString"
)

// execute снимает ПО штатным для записи способом.
//
// Ключевое: строка деинсталлятора читается ИЗ РЕЕСТРА ЭТОЙ МАШИНЫ, а не приходит
// от сервера. Сервер называет только цель; что именно запускать — решает агент
// по локальным данным. Иначе поле в задаче стало бы прямым исполнением
// произвольной командной строки от имени LocalSystem.
func execute(ctx context.Context, target collector.Software, log *slog.Logger) (string, error) {
	// Per-user установка: деинсталлятор лежит в профиле и пишет в HKCU того
	// пользователя, а служба работает от LocalSystem — её HKCU другой. Запуск
	// «сработал бы» и оставил продукт на месте. Коллектор такие записи и не
	// помечает методом, но проверяем повторно: запись могла приехать из снимка
	// агента другой версии.
	if target.Scope == collector.ScopeUser {
		return "", fmt.Errorf("установка в профиль пользователя не снимается из-под службы (LocalSystem)")
	}

	switch target.UninstallMethod {
	case collector.UninstallMSI:
		// ProductCode — это имя подключа; коллектор кладёт его в UninstallID и
		// уже проверил, что это валидный GUID.
		if !looksLikeProductCode(target.UninstallID) {
			return "", fmt.Errorf("ключ продукта %q не похож на ProductCode", target.UninstallID)
		}
		args := []string{"/x", target.UninstallID, "/qn", "/norestart"}
		log.Info("uninstall: msiexec", slog.String("product_code", target.UninstallID))
		return runCommand(ctx, msiexecName, args, nil)

	case collector.UninstallWindowsQuiet:
		argv, err := quietArgv(target.UninstallID)
		if err == nil {
			log.Info("uninstall: quiet", slog.String("exe", argv[0]))
			return runCommand(ctx, argv[0], argv[1:], nil)
		}
		if !errors.Is(err, errNoSilent) {
			return "", err
		}
		// Тихой команды нет: открываем штатный деинсталлятор на экране пользователя.
		return launchInteractive(target.UninstallID, log)

	default:
		return "", fmt.Errorf("способ снятия %q не поддержан на Windows", target.UninstallMethod)
	}
}

// quietArgv — команда тихого снятия: либо QuietUninstallString вендора, либо (когда его
// нет) та, что выводится из UninstallString для Inno Setup / NSIS. Всё — из реестра этой
// машины, сервер называет только цель.
func quietArgv(keyName string) ([]string, error) {
	if line, err := readQuietUninstallString(keyName); err == nil {
		argv, derr := windows.DecomposeCommandLine(line)
		if derr != nil || len(argv) == 0 {
			return nil, fmt.Errorf("строка тихого снятия из реестра неразбираема: %v", derr)
		}
		return argv, nil
	}
	us, err := readRegistryString(keyName, "UninstallString")
	if err != nil {
		return nil, err
	}
	if argv, ok := silentuninstall.Derive(us, readFileHead); ok {
		return argv, nil
	}
	return nil, fmt.Errorf("%w: %q", errNoSilent, keyName)
}

// errNoSilent — тихой команды вывести нельзя; не сбой, а повод открыть деинсталлятор в
// сеансе пользователя.
var errNoSilent = errors.New("тихая команда снятия недоступна")

// launchInteractive запускает UninstallString из реестра этой машины в активной
// консольной сессии (под SYSTEM — как захватчик экрана, чтобы права и UAC не мешали) и НЕ
// ждёт его завершения: человек доводит визард через удалённый экран. Итог определяет
// повторный снимок ПО, так что открытый визард честно даст «осталось на машине».
func launchInteractive(keyName string, log *slog.Logger) (string, error) {
	us, err := readRegistryString(keyName, "UninstallString")
	if err != nil {
		return "", err
	}
	argv, err := windows.DecomposeCommandLine(us)
	if err != nil || len(argv) == 0 {
		return "", fmt.Errorf("UninstallString из реестра неразбираем: %v", err)
	}
	h, err := winsession.LaunchInActiveSessionOpts(argv[0], argv[1:], winsession.Options{AsSystem: true})
	if err != nil {
		if errors.Is(err, winsession.ErrNoActiveSession) {
			return "", fmt.Errorf("тихого снятия нет, а на машине никто не вошёл в систему: деинсталлятор негде показать")
		}
		return "", fmt.Errorf("не удалось открыть деинсталлятор в сеансе пользователя: %w", err)
	}
	_ = windows.CloseHandle(h)
	log.Info("uninstall: деинсталлятор открыт в сеансе пользователя", slog.String("exe", argv[0]))
	return "деинсталлятор открыт на экране пользователя — завершите удаление через удалённый экран", nil
}

// readFileHead — начало файла для поиска подписи установщика (см. silentuninstall).
func readFileHead(path string, max int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, int64(max)))
	return b
}

// readQuietUninstallString достаёт строку тихого снятия из той же ветки ARP, из
// которой коллектор увидел запись. Обе ветки (нативная и WOW6432Node) —
// 32-битный продукт на 64-битной ОС живёт во второй.
func readQuietUninstallString(keyName string) (string, error) {
	return readRegistryString(keyName, quietUninstallString)
}

func readRegistryString(keyName, value string) (string, error) {
	if !validRegistryKeyName(keyName) {
		return "", fmt.Errorf("недопустимый ключ записи %q", keyName)
	}
	for _, root := range []string{uninstallKeyPath, uninstallKeyPathWow} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, root+`\`+keyName, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue(value)
		k.Close()
		if err == nil && strings.TrimSpace(v) != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("в реестре нет значения %s для %q", value, keyName)
}

// looksLikeProductCode и validRegistryKeyName живут в validate.go без build-тега:
// это чистые строковые проверки, и гоняться они обязаны на каждом прогоне
// тестов, а не только на Windows.
