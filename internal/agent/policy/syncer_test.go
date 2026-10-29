package policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func tmpFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "forbidden.txt")
}

// syncOnce пишет в кэш только запрещённые правила с непустым именем, с версией.
func TestSyncOnce_WritesForbiddenRules(t *testing.T) {
	file := tmpFile(t)
	s := &Syncer{File: file, Log: quietLog(), Interval: time.Hour}
	s.fetch = func(context.Context, int64) (*pb.FetchPolicyResponse, error) {
		return &pb.FetchPolicyResponse{
			Version: 42,
			Rules: []*pb.SoftwarePolicyRule{
				{RuleType: pb.PolicyRuleType_POLICY_RULE_TYPE_FORBIDDEN, SoftwareName: "evil.exe"},
				{RuleType: pb.PolicyRuleType_POLICY_RULE_TYPE_ALLOWED, SoftwareName: "good.app"},
				{RuleType: pb.PolicyRuleType_POLICY_RULE_TYPE_FORBIDDEN, SoftwareName: ""}, // отфильтровать
			},
		}, nil
	}

	s.syncOnce(context.Background())

	if v := readVersion(file); v != 42 {
		t.Errorf("версия в кэше = %d, ожидалось 42", v)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "evil.exe") {
		t.Error("запрещённое ПО 'evil.exe' не записано")
	}
	if strings.Contains(body, "good.app") {
		t.Error("разрешённое ПО 'good.app' не должно попадать в список запрещённого")
	}
}

// Ответ Unchanged не должен переписывать существующий кэш.
func TestSyncOnce_Unchanged_KeepsCache(t *testing.T) {
	file := tmpFile(t)
	if err := writeList(file, 7, []string{"old.exe"}); err != nil {
		t.Fatalf("подготовка кэша: %v", err)
	}
	before, _ := os.ReadFile(file)

	s := &Syncer{File: file, Log: quietLog(), Interval: time.Hour}
	var gotKnown int64 = -1
	s.fetch = func(_ context.Context, known int64) (*pb.FetchPolicyResponse, error) {
		gotKnown = known
		return &pb.FetchPolicyResponse{Unchanged: true}, nil
	}

	s.syncOnce(context.Background())

	if gotKnown != 7 {
		t.Errorf("FetchPolicy получил known_version=%d, ожидалось 7", gotKnown)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Error("кэш переписан при ответе Unchanged")
	}
}

// Ошибка FetchPolicy не должна создавать/портить кэш.
func TestSyncOnce_FetchError_NoWrite(t *testing.T) {
	file := tmpFile(t)
	s := &Syncer{File: file, Log: quietLog(), Interval: time.Hour}
	s.fetch = func(context.Context, int64) (*pb.FetchPolicyResponse, error) {
		return nil, errors.New("rpc failed")
	}

	s.syncOnce(context.Background())

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("кэш создан несмотря на ошибку FetchPolicy")
	}
}

// Run выполняет первую синхронизацию после initialDelay и завершается по отмене.
func TestRun_SyncsThenStops(t *testing.T) {
	old := initialDelay
	initialDelay = time.Millisecond
	defer func() { initialDelay = old }()

	file := tmpFile(t)
	s := &Syncer{File: file, Log: quietLog(), Interval: time.Hour}
	synced := make(chan struct{}, 1)
	s.fetch = func(context.Context, int64) (*pb.FetchPolicyResponse, error) {
		select {
		case synced <- struct{}{}:
		default:
		}
		return &pb.FetchPolicyResponse{Version: 1, Rules: []*pb.SoftwarePolicyRule{
			{RuleType: pb.PolicyRuleType_POLICY_RULE_TYPE_FORBIDDEN, SoftwareName: "x"},
		}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	select {
	case <-synced:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("Run не выполнил первую синхронизацию")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился после отмены контекста")
	}
}
