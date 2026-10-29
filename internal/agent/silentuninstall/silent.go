// Package silentuninstall выводит тихую команду снятия для установщиков, которые не
// пишут QuietUninstallString, но однозначно узнаются по самому деинсталлятору:
// Inno Setup (unins000.exe) и NSIS (Nullsoft). Пакет чистый: без реестра и без
// запуска процессов, поэтому проверяется на любой ОС.
package silentuninstall

import (
	"bytes"
	"regexp"
	"strings"
)

// maxHead — сколько байт деинсталлятора просматривается в поисках подписи установщика.
// Подписи лежат в заголовке/данных небольшого exe; больше читать незачем.
const maxHead = 4 << 20

var innoName = regexp.MustCompile(`(?i)^unins\d{3}\.exe$`)

// ReadHead отдаёт начало файла (не больше maxHead байт). nil — прочитать не вышло.
type ReadHead func(path string, max int) []byte

// Derive разбирает UninstallString и, если деинсталлятор — Inno Setup или NSIS,
// возвращает argv тихого снятия. ok=false — «снять тихо нечем»: сомнение всегда в
// эту сторону, иначе у оператора появилась бы кнопка, зависающая на визарде в
// session 0.
func Derive(uninstallString string, read ReadHead) (argv []string, ok bool) {
	exe, rest, ok := splitCommand(uninstallString)
	if !ok || !strings.HasSuffix(strings.ToLower(exe), ".exe") {
		return nil, false
	}
	// Лишние аргументы (/uninstall, ключи вендора) не переносим: берём только свой набор,
	// иначе непредсказуемый ключ мог бы вернуть интерактив.
	_ = rest
	base := exe[strings.LastIndexAny(exe, `\/`)+1:]
	var head []byte
	if read != nil {
		head = read(exe, maxHead)
	}
	switch {
	case innoName.MatchString(base) || bytes.Contains(head, []byte("Inno Setup")):
		return []string{exe, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}, true
	case bytes.Contains(head, []byte("NullsoftInst")):
		// NSIS-деинсталлятор копирует себя во временный каталог и сразу возвращается, так что
		// «успех» задачи означает «запущено», а не «закончено»: список ПО обновится
		// следующим снимком. Ключ _?= (синхронный режим) не берём: ему нужен путь без кавычек,
		// а Go при запуске процесса квотирует аргументы с пробелами.
		return []string{exe, "/S"}, true
	}
	return nil, false
}

// splitCommand делит команду на исполняемый файл и остаток. Путь либо в кавычках, либо
// до первого пробела после «.exe» (пути без кавычек с пробелами пишут многие старые
// установщики).
func splitCommand(s string) (exe, rest string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", "", false
		}
		return s[1 : 1+end], strings.TrimSpace(s[2+end:]), true
	}
	low := strings.ToLower(s)
	i := strings.Index(low, ".exe")
	if i < 0 {
		return "", "", false
	}
	return s[:i+4], strings.TrimSpace(s[i+4:]), true
}
