package scripts

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// dedupSet — персистентное множество ключей уже выполненных on_connect-запусков.
// Ключ — "policy_id@version": один и тот же скрипт не перезапускается на каждом
// реконнекте, но bump версии политики (новый updated_at) даёт новый ключ → запуск.
// Переживает рестарт агента (как command.seenSet). Запись best-effort.
type dedupSet struct {
	mu   sync.Mutex
	m    map[string]struct{}
	path string
}

func loadDedupSet(path string) *dedupSet {
	s := &dedupSet{m: make(map[string]struct{}), path: path}
	if path == "" {
		return s
	}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k := strings.TrimSpace(sc.Text()); k != "" {
			s.m[k] = struct{}{}
		}
	}
	return s
}

// markIfNew помечает ключ выполненным. true — ключ новый (можно запускать).
func (s *dedupSet) markIfNew(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; ok {
		return false
	}
	s.m[key] = struct{}{}
	if s.path != "" {
		if f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, key)
			f.Close()
		}
	}
	return true
}
