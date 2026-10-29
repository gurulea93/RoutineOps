package security

import (
	"bufio"
	"os"
	"strings"
)

// loadForbidden читает список запрещённого ПО: по одному шаблону в строке,
// пустые строки и комментарии (#) игнорируются, регистр приводится к нижнему.
// Шаблон сопоставляется как подстрока имени/командной строки процесса.
// Отсутствие файла — не ошибка (пустой список).
func loadForbidden(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.ToLower(line))
	}
	return out, sc.Err()
}
