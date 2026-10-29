// Package policy синхронизирует политики ПО с сервера (FetchPolicy) и хранит их
// локально для Security Monitor. Кэш = тот же файл со списком запрещённого ПО,
// который читает internal/agent/security: запрещённые имена построчно + версия в
// строке-комментарии "# version: <unix>". Файл переживает оффлайн (последняя
// синхронизированная политика применяется без связи с сервером — CONTEXT §7).
package policy

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
	pb "github.com/Floodww/RoutineOps/proto"
)

const (
	syncTimeout = 30 * time.Second
	versionPfx  = "# version: "
)

// initialDelay — задержка перед первой синхронизацией (даёт heartbeat
// зарегистрировать устройство). Var, чтобы тесты сокращали ожидание.
var initialDelay = 3 * time.Second

// Syncer периодически тянет политику ПО и пишет её в File.
type Syncer struct {
	Interval time.Duration
	File     string // общий с Security Monitor файл списка запрещённого ПО
	Dialer   *transport.Dialer
	Log      *slog.Logger

	// fetch тянет политику с сервера. Поле (а не прямой dial+RPC), чтобы тесты
	// подставляли фейковый ответ. По умолчанию — dialAndFetch.
	fetch func(ctx context.Context, knownVersion int64) (*pb.FetchPolicyResponse, error)
}

// Run синхронизирует политику через initialDelay, затем каждые Interval.
func (s *Syncer) Run(ctx context.Context) {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.syncOnce(ctx)
			timer.Reset(s.Interval)
		}
	}
}

func (s *Syncer) syncOnce(ctx context.Context) {
	known := readVersion(s.File) // 0 = нет кэша

	if s.fetch == nil {
		s.fetch = s.dialAndFetch
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	resp, err := s.fetch(ctx, known)
	if err != nil {
		s.Log.Error("policy: FetchPolicy", slog.Any("error", err))
		return
	}
	if resp.GetUnchanged() {
		s.Log.Debug("policy: без изменений", slog.Int64("version", known))
		return
	}

	var forbidden []string
	for _, r := range resp.GetRules() {
		if r.GetRuleType() == pb.PolicyRuleType_POLICY_RULE_TYPE_FORBIDDEN && r.GetSoftwareName() != "" {
			forbidden = append(forbidden, r.GetSoftwareName())
		}
	}
	if err := writeList(s.File, resp.GetVersion(), forbidden); err != nil {
		s.Log.Error("policy: запись кэша", slog.String("file", s.File), slog.Any("error", err))
		return
	}
	s.Log.Info("политика ПО обновлена",
		slog.Int64("version", resp.GetVersion()), slog.Int("forbidden", len(forbidden)))
}

// dialAndFetch — продакшн-реализация fetch: dial + unary FetchPolicy.
func (s *Syncer) dialAndFetch(ctx context.Context, knownVersion int64) (*pb.FetchPolicyResponse, error) {
	conn, err := s.Dialer.Dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return pb.NewAgentServiceClient(conn).FetchPolicy(ctx, &pb.FetchPolicyRequest{KnownVersion: knownVersion})
}

// readVersion читает "# version: <unix>" из файла; 0 если нет файла/строки.
func readVersion(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, versionPfx); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// writeList атомарно пишет версию + список запрещённого ПО (tmp + rename), чтобы
// Security Monitor не прочитал файл на середине записи.
func writeList(path string, version int64, forbidden []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%d\n", versionPfx, version)
	fmt.Fprintln(&b, "# синхронизировано с сервера (FetchPolicy) — не редактировать вручную")
	for _, name := range forbidden {
		fmt.Fprintln(&b, name)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".policy-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // на случай ошибки до rename

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
