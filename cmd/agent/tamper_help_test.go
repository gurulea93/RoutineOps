package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// TestTamperUsagePerPlatform — «не врать оператору»: справка по tamper-командам
// должна описывать РЕАЛЬНЫЙ механизм платформы, а не единый Windows-текст.
func TestTamperUsagePerPlatform(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		u := tamperUsage(goos)
		for _, cmd := range []string{"tamper-status", "tamper-disarm", "tamper-cleanup"} {
			if !strings.Contains(u, cmd) {
				t.Errorf("%s: справка не упоминает %s:\n%s", goos, cmd, u)
			}
		}
	}
	darwin := tamperUsage("darwin")
	for _, want := range []string{"schg", "root"} {
		if !strings.Contains(darwin, want) {
			t.Errorf("darwin-справка не содержит %q:\n%s", want, darwin)
		}
	}
	for _, lie := range []string{"безопасн", "SafeBoot", "HKLM", "uninstall.bat"} {
		if strings.Contains(darwin, lie) {
			t.Errorf("darwin-справка врёт про Windows-механику (%q):\n%s", lie, darwin)
		}
	}
	win := tamperUsage("windows")
	for _, want := range []string{"безопасн", "SafeBoot"} {
		if !strings.Contains(win, want) {
			t.Errorf("windows-справка не содержит %q:\n%s", want, win)
		}
	}
	linux := tamperUsage("linux")
	if !strings.Contains(linux, "не реализована") && !strings.Contains(linux, "не поддерживается") {
		t.Errorf("linux-справка должна честно сказать про отсутствие защиты:\n%s", linux)
	}
	if strings.Contains(linux, "schg") || strings.Contains(linux, "SafeBoot") {
		t.Errorf("linux-справка не должна обещать защиту:\n%s", linux)
	}
}

// TestPrintUsageIncludesTamperBlockForHostOS — верб %s подставлен, справка не
// рассинхронизирована с блоком tamper.
func TestPrintUsageIncludesTamperBlockForHostOS(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	if !strings.Contains(out, tamperUsage(runtime.GOOS)) {
		t.Errorf("справка не содержит tamper-блок для %s:\n%s", runtime.GOOS, out)
	}
	for _, bad := range []string{"%!s(MISSING)", "%s\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("в справке остался неподставленный verb (%q):\n%s", bad, out)
		}
	}
}

// TestTamperStatusReportDarwinNoWindowsLie — на маке отчёт про schg, без HKLM/SafeBoot.
func TestTamperStatusReportDarwinNoWindowsLie(t *testing.T) {
	armed := tamperStatusReport("darwin", 1, 0, false)
	for _, want := range []string{"schg=1", "tamper-disarm", "uninstall"} {
		if !strings.Contains(armed, want) {
			t.Errorf("darwin-отчёт не содержит %q:\n%s", want, armed)
		}
	}
	for _, lie := range []string{"HKLM", "SafeBoot", "безопасн", "uninstall.bat"} {
		if strings.Contains(armed, lie) {
			t.Errorf("darwin-отчёт врёт про Windows (%q):\n%s", lie, armed)
		}
	}
	if !strings.Contains(tamperStatusReport("darwin", 0, 0, false), "Защита СНЯТА") {
		t.Errorf("darwin: при нулях ждали 'Защита СНЯТА'")
	}
}

// TestTamperStatusReportWindowsUnchanged — регрессия: Windows-строка байт-в-байт та же,
// на неё завязаны полевые инструкции и процедура uninstall.bat.
func TestTamperStatusReportWindowsUnchanged(t *testing.T) {
	out := tamperStatusReport("windows", 1, 1, false)
	if !strings.Contains(out, "TamperProtection=1 SafeBootGuard=1 safe_mode=false") {
		t.Errorf("Windows-формат изменился:\n%s", out)
	}
	if !strings.Contains(out, "HKLM\\SOFTWARE\\RoutineOps\\Agent") {
		t.Errorf("Windows-подсказка потеряла HKLM-путь:\n%s", out)
	}
}

// TestTamperStatusReportLinux — честно про отсутствие защиты, без обещаний disarm.
func TestTamperStatusReportLinux(t *testing.T) {
	out := tamperStatusReport("linux", 0, 0, false)
	if !strings.Contains(out, "не реализована") {
		t.Errorf("linux-отчёт должен честно сказать про отсутствие защиты:\n%s", out)
	}
}

// TestTamperDisarmDoneMsg — послесловие disarm: на маке без reboot/uninstall.bat.
func TestTamperDisarmDoneMsg(t *testing.T) {
	darwin := tamperDisarmDoneMsg("darwin")
	for _, lie := range []string{"перезагруз", "uninstall.bat"} {
		if strings.Contains(darwin, lie) {
			t.Errorf("darwin disarm-послесловие врёт про Windows (%q): %s", lie, darwin)
		}
	}
	win := tamperDisarmDoneMsg("windows")
	for _, want := range []string{"перезагруз", "uninstall.bat"} {
		if !strings.Contains(win, want) {
			t.Errorf("windows disarm-послесловие потеряло %q: %s", want, win)
		}
	}
}
