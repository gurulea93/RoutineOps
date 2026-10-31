//go:build screen && windows

package screen

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// rootEntries — диски машины («C:\», «D:\»…).
func rootEntries() []fsEntry {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []fsEntry
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, fsEntry{Name: string(rune('A'+i)) + `:\`, Dir: true})
		}
	}
	return out
}

// userHomeDir — Рабочий стол сотрудника за консолью. Захватчик работает от SYSTEM, поэтому
// свой профиль не годится: берём токен пользователя активной консольной сессии.
func userHomeDir() string {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(windows.WTSGetActiveConsoleSessionId(), &tok); err != nil {
		return ""
	}
	defer tok.Close()
	dir, err := tok.GetUserProfileDirectory()
	if err != nil || dir == "" {
		return ""
	}
	desk := filepath.Join(dir, "Desktop")
	if st, err := os.Stat(desk); err == nil && st.IsDir() {
		return desk
	}
	return dir
}
