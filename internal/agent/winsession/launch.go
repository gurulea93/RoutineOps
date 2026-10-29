// Package winsession запускает процессы в активной консольной сессии пользователя
// из контекста службы (Windows, session 0, LocalSystem). Служба не имеет рабочего
// стола и сама GUI рисовать не может, но как LocalSystem имеет право стартовать
// процесс в сессии залогиненного пользователя (CreateProcessAsUser). Этот общий
// механизм поднимает и полноэкранный лок-оверлей, и иконку трея сразу после
// установки. Вне Windows пакет содержит только тестируемую сборку командной строки.
package winsession

import "strings"

// buildCmdLine собирает командную строку для CreateProcessAsUser: путь к бинарю в
// кавычках (терпит пробелы в "C:\Program Files\…") + аргументы через пробел.
// Вынесено без build-тега, чтобы покрыть тестом на любой платформе.
func buildCmdLine(exe string, args []string) string {
	var b strings.Builder
	b.WriteByte('"')
	b.WriteString(exe)
	b.WriteByte('"')
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(a)
	}
	return b.String()
}
