package service

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLogFilePath: путь к логу службы абсолютный и оканчивается на agent.log
// (имя файла стабильно на всех ОС, диагностика ищет именно его).
func TestLogFilePath(t *testing.T) {
	p := LogFilePath()
	if p == "" {
		t.Fatal("LogFilePath пустой")
	}
	if !filepath.IsAbs(p) {
		t.Errorf("LogFilePath не абсолютный: %q", p)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), "/agent.log") {
		t.Errorf("LogFilePath должен оканчиваться на agent.log: %q", p)
	}
}
