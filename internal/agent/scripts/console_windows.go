//go:build windows

package scripts

import (
	"os"
	"os/exec"
	"strings"
)

// osConsoleUser — интерактивный пользователь Windows (без домена). Best-effort.
func osConsoleUser() string {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance Win32_ComputerSystem).UserName").Output()
	if err != nil {
		return os.Getenv("USERNAME")
	}
	u := strings.TrimSpace(string(out))
	if i := strings.LastIndex(u, `\`); i >= 0 {
		u = u[i+1:] // DOMAIN\user -> user
	}
	return u
}
