//go:build windows

package security

import (
	"encoding/csv"
	"os/exec"
	"strconv"
	"strings"
)

// listProcesses перечисляет процессы через tasklist (CSV без заголовка).
// На Этапе 6 достаточно имени образа; ETW (более точный путь) — позже.
//
// chcp 65001 переводит консоль в UTF-8 ДО запуска tasklist, иначе образы с
// не-ASCII в имени приходят в OEM-кодировке (CP866/CP1251) и в UI это ромбики.
func listProcesses() ([]Process, error) {
	out, err := exec.Command("cmd", "/c", "chcp 65001>nul & tasklist /fo csv /nh").Output()
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(out), "\ufeff")))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var ps []Process
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(row[1]))
		ps = append(ps, Process{PID: pid, Name: row[0], Cmd: row[0]})
	}
	return ps, nil
}
