package api

import "testing"

func TestSanitizeSoftwareName(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"Google Chrome", "Google Chrome", true},
		{"  TeamViewer  ", "TeamViewer", true}, // trim
		{"", "", false},                        // пусто
		{"   ", "", false},                     // только пробелы
		{"#1 Torrent", "", false},              // ведущий # = комментарий в кэше агента
		{"evil\nchrome", "", false},            // \n расщепил бы на два паттерна
		{"tab\there", "", false},               // управляющий символ
		{"back\rspace", "", false},             // CR
	}
	for _, c := range cases {
		got, ok := sanitizeSoftwareName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("sanitizeSoftwareName(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
