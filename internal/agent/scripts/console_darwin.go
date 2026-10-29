//go:build darwin

package scripts

import (
	"os/exec"
	"strings"
)

// osConsoleUser — текущий интерактивный пользователь macOS. "" если только root
// (никто не вошёл в графическую сессию).
func osConsoleUser() string {
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(out))
	if u == "root" {
		return ""
	}
	return u
}
