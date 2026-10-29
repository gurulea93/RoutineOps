//go:build screen

package screen

import (
	"strings"
	"unicode"
)

// noticeText — строка плашки наблюдения. Оператор и повод приходят с сервера, поэтому
// управляющие символы заменяются пробелами (иначе перевод строки подделал бы вторую
// «официальную» строку), а длина ограничивается: плашка однострочная.
//
// Две языковые версии подряд: сотрудники читают и по-русски, и по-румынски, а выбирать
// язык плашка не умеет и не должна — она показывается без участия сотрудника.
func noticeText(operator, reason string) string {
	return "Экран просматривается / Ecranul este vizualizat: " +
		clip(cleanLine(operator), 60) + " — " + clip(cleanLine(reason), 120)
}

func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
}

func clip(s string, max int) string {
	if rs := []rune(s); len(rs) > max {
		return string(rs[:max-1]) + "…"
	}
	return s
}
