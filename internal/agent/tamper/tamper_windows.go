//go:build windows

package tamper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// regPath — ветка с флагами защиты. HKLM ⇒ запись только под админом. Именно эти
	// значения админ выставляет в 0 из безопасного режима, чтобы разблокировать
	// удаление (см. build/msi/uninstall.bat и docs/tamper-protection.md).
	regPath       = `SOFTWARE\RoutineOps\Agent`
	valProtection = "TamperProtection"
	valSafeBoot   = "SafeBootGuard"

	// svcName должно совпадать с service.Name. Пакет tamper намеренно не импортирует
	// service (чтобы не тащить SCM-зависимости в платформенный код) — дублируем имя.
	svcName         = "RoutineOps-agent"
	safeBootMinimal = `SYSTEM\CurrentControlSet\Control\SafeBoot\Minimal\` + svcName
	safeBootNetwork = `SYSTEM\CurrentControlSet\Control\SafeBoot\Network\` + svcName

	// enforceInterval — период восстановления SafeBoot-ключей и флагов в обычном
	// режиме. 30с — компромисс: достаточно часто против ручной правки реестра, не
	// нагружает систему.
	enforceInterval = 30 * time.Second
)

// errNotSafeMode возвращает Disarm, если вызван не в безопасном режиме: там сброс
// флагов бессмысленен (enforce-цикл тут же перевзведёт их обратно).
var errNotSafeMode = errors.New("разоружение работает только в безопасном режиме Windows")

// SM_CLEANBOOT (67) для GetSystemMetrics: 0 — обычная загрузка, 1 — безопасный
// режим (минимальный), 2 — безопасный режим с поддержкой сети.
const smCleanBoot = 67

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

// cleanBoot возвращает код режима загрузки (SM_CLEANBOOT).
func cleanBoot() int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(smCleanBoot))
	return int(r)
}

// SafeMode сообщает, загружена ли ОС в безопасном режиме (любой вариант).
func SafeMode() bool { return cleanBoot() != 0 }

// readFlag читает DWORD-флаг из regPath. ok=false, если ключа/значения нет.
func readFlag(name string) (val uint32, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regPath, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// writeFlag создаёт regPath при необходимости и пишет DWORD-флаг.
func writeFlag(name string, val uint32) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("открытие %s: %w", regPath, err)
	}
	defer k.Close()
	return k.SetDWordValue(name, val)
}

// protectionOn — защита взведена, только если ОБА флага присутствуют и != 0.
// Обнуление любого из них (в безопасном режиме) разоружает агента.
func protectionOn() bool {
	p, okP := readFlag(valProtection)
	g, okG := readFlag(valSafeBoot)
	return okP && okG && p != 0 && g != 0
}

// safeBootRegister регистрирует службу в SafeBoot\Minimal и \Network. Значение по
// умолчанию подключа = "Service" — так SCM запускает службу и в безопасном режиме.
func safeBootRegister() error {
	for _, path := range []string{safeBootMinimal, safeBootNetwork} {
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("создание %s: %w", path, err)
		}
		err = k.SetStringValue("", "Service")
		k.Close()
		if err != nil {
			return fmt.Errorf("запись значения %s: %w", path, err)
		}
	}
	return nil
}

// safeBootUnregister снимает регистрацию службы в безопасном режиме (best-effort).
func safeBootUnregister() {
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, safeBootMinimal)
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, safeBootNetwork)
}

// Arm взводит защиту: пишет флаги=1 и регистрирует службу в SafeBoot. Вызывается на
// install под админом. Идемпотентна.
func Arm(log *slog.Logger) error {
	if err := writeFlag(valProtection, 1); err != nil {
		return fmt.Errorf("взвод %s: %w", valProtection, err)
	}
	if err := writeFlag(valSafeBoot, 1); err != nil {
		return fmt.Errorf("взвод %s: %w", valSafeBoot, err)
	}
	if err := safeBootRegister(); err != nil {
		return fmt.Errorf("регистрация в SafeBoot: %w", err)
	}
	log.Info("tamper-protection взведена",
		slog.String("reg", `HKLM\`+regPath),
		slog.String("note", "снятие только из безопасного режима + uninstall.bat"))
	return nil
}

// Disarm выставляет флаги в 0, разрешая удаление. Работает ТОЛЬКО в безопасном
// режиме — в обычном enforce-цикл сразу перевзведёт флаги обратно, поэтому без
// безопасного режима разоружение бессмысленно (возвращаем errNotSafeMode).
func Disarm() error {
	if !SafeMode() {
		return errNotSafeMode
	}
	if err := writeFlag(valProtection, 0); err != nil {
		return err
	}
	return writeFlag(valSafeBoot, 0)
}

// Unlock — no-op: на Windows защита не трогает атрибуты файлов (флаги в HKLM +
// SafeBoot-регистрация + exe, залоченный запущенной службой), поэтому установщику
// (MSI) снимать с файлов нечего. Функция существует, чтобы кросс-платформенные
// install-пути звали её без build-тегов.
func Unlock(...string) error { return nil }

// Cleanup снимает SafeBoot-регистрацию и удаляет ветку флагов. Вызывается
// деинсталлятором (uninstall.bat) уже после разоружения и reboot в обычный режим.
func Cleanup() {
	safeBootUnregister()
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, regPath)
}

// Status возвращает текущие значения флагов и признак безопасного режима — для
// команды tamper-status (диагностика/процедура снятия).
func Status() (protection, safeBoot uint32, safeMode bool) {
	p, _ := readFlag(valProtection)
	g, _ := readFlag(valSafeBoot)
	return p, g, SafeMode()
}

// Enforce — фоновый сторож защиты, запускается рабочим циклом службы. Решение о
// поведении принимается один раз на старте по режиму загрузки и флагам (см. модель
// в tamper.go), дальше — периодическое восстановление. Блокируется до ctx.Done.
func Enforce(ctx context.Context, log *slog.Logger) {
	switch {
	case cleanBoot() != 0:
		// Безопасный режим — окно разоружения: служба запущена и держит exe
		// залоченным, но флаги НЕ перевзводим, чтобы админ мог выставить их в 0.
		log.Warn("безопасный режим — enforce tamper-protection приостановлен (окно разоружения)",
			slog.Int("clean_boot", cleanBoot()))
		return
	case !protectionOn():
		// Флаги обнулены в безопасном режиме и прочитаны как 0 на старте → пассивный
		// режим: ничего не перевзводим, удаление разрешено.
		log.Info("tamper-protection разоружена — пассивный режим, удаление разрешено")
		return
	}

	log.Info("tamper-protection активна — слежу за SafeBoot-ключами и флагами",
		slog.Duration("interval", enforceInterval))
	reassert(log)
	t := time.NewTicker(enforceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reassert(log)
		}
	}
}

// reassert восстанавливает SafeBoot-ключи и возвращает флаги в 1, если их обнулили
// в обычном режиме (durably обнулить можно только из безопасного — см. Enforce).
func reassert(log *slog.Logger) {
	if err := safeBootRegister(); err != nil {
		log.Warn("не удалось восстановить SafeBoot-ключи", slog.Any("error", err))
	}
	if p, ok := readFlag(valProtection); !ok || p == 0 {
		if err := writeFlag(valProtection, 1); err != nil {
			log.Warn("не удалось перевзвести "+valProtection, slog.Any("error", err))
		} else {
			log.Warn("обнаружен сброс tamper-флага в обычном режиме — перевзведён",
				slog.String("flag", valProtection))
		}
	}
	if g, ok := readFlag(valSafeBoot); !ok || g == 0 {
		if err := writeFlag(valSafeBoot, 1); err != nil {
			log.Warn("не удалось перевзвести "+valSafeBoot, slog.Any("error", err))
		} else {
			log.Warn("обнаружен сброс tamper-флага в обычном режиме — перевзведён",
				slog.String("flag", valSafeBoot))
		}
	}
}
