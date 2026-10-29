//go:build !windows && !linux

package security

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// listProcesses перечисляет процессы через ps (macOS/BSD), без спец-прав. На
// macOS comm= отдаёт полный путь бинаря без усечения; Linux сюда не попадает —
// там ядро режет comm до 15 символов, поэтому у него свой procfs-путь
// (proc_linux.go, см. procfsProcesses).
func listProcesses() ([]Process, error) {
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var ps []Process
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		cmd := strings.Join(fields[1:], " ")
		ps = append(ps, Process{PID: pid, Name: filepath.Base(cmd), Cmd: cmd})
	}
	return ps, nil
}
