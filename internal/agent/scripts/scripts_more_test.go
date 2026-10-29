package scripts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/scriptenc"
	pb "github.com/Floodww/RoutineOps/proto"
)

// interpreterCommand сопоставляет имя интерпретатора с командой; неизвестный → пусто.
func TestInterpreterCommandMapping(t *testing.T) {
	cases := []struct {
		interp  string
		wantCmd string
	}{
		{"", "sh"},
		{"shell", "sh"},
		{"sh", "sh"},
		{"bash", "bash"},
		{"python", "python3"},
		{"python3", "python3"},
		{"definitely-not-an-interpreter", ""},
	}
	for _, c := range cases {
		cmd, args := interpreterCommand(c.interp, "echo hi")
		if cmd != c.wantCmd {
			t.Errorf("interpreterCommand(%q): cmd=%q, ожидали %q", c.interp, cmd, c.wantCmd)
		}
		if c.wantCmd != "" && (len(args) == 0 || args[len(args)-1] != "echo hi") {
			t.Errorf("interpreterCommand(%q): содержимое скрипта не проброшено в args: %v", c.interp, args)
		}
		if c.wantCmd == "" && args != nil {
			t.Errorf("interpreterCommand(%q): для неизвестного ожидали nil args, got %v", c.interp, args)
		}
	}

	// powershell/pwsh — команда зависит от ОС, но содержимое всегда последним
	// аргументом, предварённым UTF-8-префиксом (RU-Windows: иначе вывод в
	// OEM/ANSI ломает Marshal(ScriptResult)).
	cmd, args := interpreterCommand("powershell", "Get-Date")
	wantPS := "pwsh"
	if runtime.GOOS == "windows" {
		wantPS = "powershell"
	}
	if cmd != wantPS {
		t.Errorf("powershell на %s: cmd=%q, ожидали %q", runtime.GOOS, cmd, wantPS)
	}
	last := args[len(args)-1]
	if !strings.HasSuffix(last, "Get-Date") {
		t.Errorf("powershell: содержимое не проброшено последним аргументом: %v", args)
	}
	if !strings.HasPrefix(last, scriptenc.PSUTF8Prefix) {
		t.Errorf("powershell: отсутствует UTF-8-префикс в аргументе: %q", last)
	}
}

// NewRunner собирает Runner с боевым exec по умолчанию.
func TestNewRunnerWired(t *testing.T) {
	r := NewRunner(func(string, []byte) error { return nil }, discardLog())
	if r == nil {
		t.Fatal("NewRunner вернул nil")
	}
	if r.exec == nil {
		t.Fatal("exec не установлен")
	}
	if r.enqueue == nil {
		t.Fatal("enqueue не установлен")
	}
}

// loadDedupSet без файла → пустое множество; markIfNew персистит ключи, а
// повторная загрузка их видит.
func TestDedupSetPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedup.txt")

	s := loadDedupSet(path) // файла ещё нет
	if !s.markIfNew("p1@v1") {
		t.Fatal("первый ключ должен быть новым")
	}
	if s.markIfNew("p1@v1") {
		t.Fatal("повторный ключ не должен быть новым")
	}
	if !s.markIfNew("p1@v2") { // bump версии → новый ключ
		t.Fatal("новая версия политики должна давать новый ключ")
	}

	// Перезагрузка из файла видит оба ключа.
	s2 := loadDedupSet(path)
	if s2.markIfNew("p1@v1") || s2.markIfNew("p1@v2") {
		t.Fatal("перезагруженный dedupSet должен помнить persisted-ключи")
	}
	if !s2.markIfNew("p1@v3") {
		t.Fatal("новый ключ после перезагрузки должен считаться новым")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("dedup-файл не записан: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("dedup-файл пуст")
	}
}

// loadDedupSet с пустым путём не падает и работает как in-memory множество.
func TestLoadDedupSetNoPath(t *testing.T) {
	s := loadDedupSet("")
	if !s.markIfNew("x") || s.markIfNew("x") {
		t.Fatal("in-memory dedupSet работает некорректно")
	}
}

// osConsoleUser лишь читает текущего пользователя сессии — безопасно вызвать.
func TestOSConsoleUserSmoke(t *testing.T) {
	_ = osConsoleUser()
}

// runByID запускает политику по id (как делает cron); неизвестный id — no-op.
func TestRunByID(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "")
	m.apply([]*pb.ScriptPolicy{
		{PolicyId: "sched1", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE, Cron: "*/5 * * * *"},
	}, 1)

	m.runByID("sched1", pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
	if c := waitCall(t, fr); c.policyID != "sched1" || c.trigger != pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE {
		t.Fatalf("ожидали запуск sched1 по cron, got %+v", c)
	}

	m.runByID("несуществующий", pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE)
	noCall(t, fr)
}

// NewManager собирает Manager с боевым Runner и dedup; боевой dialer при
// конструировании не вызывается.
func TestNewManagerWiring(t *testing.T) {
	m := NewManager(nil, func(string, []byte) error { return nil }, time.Minute, "", discardLog())
	if m.runner == nil {
		t.Fatal("runner не установлен")
	}
	if m.dedup == nil {
		t.Fatal("dedup не установлен")
	}
	if m.interval != time.Minute {
		t.Fatalf("interval=%v", m.interval)
	}
}

// Run: первый поллинг подтягивает набор, выставляет ready и доигрывает отложенный
// OnConnect (пришедший до готовности), затем завершается по отмене контекста.
func TestRunProcessesPendingOnConnect(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := &Manager{
		interval: 10 * time.Millisecond,
		log:      discardLog(),
		runner:   fr,
		dedup:    loadDedupSet(""),
		fetch: func(context.Context, int64) (*pb.FetchScriptPoliciesResponse, error) {
			return &pb.FetchScriptPoliciesResponse{
				Version:  1,
				Policies: []*pb.ScriptPolicy{{PolicyId: "oc", Trigger: pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT, UpdatedAt: 1}},
			}, nil
		},
	}

	m.OnConnect() // ready ещё false → запрос откладывается

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	// После первого poll готовность выставится и отложенный OnConnect доиграется.
	if c := waitCall(t, fr); c.policyID != "oc" {
		t.Fatalf("ожидали отложенный on_connect oc, got %+v", c)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run не завершился после отмены контекста")
	}
}

// poll логирует и не падает, если FetchScriptPolicies вернул ошибку (нет связи).
func TestPollFetchError(t *testing.T) {
	fr := &fakeRunner{ch: make(chan runCall, 8)}
	m := newTestManager(fr, "")
	m.fetch = func(context.Context, int64) (*pb.FetchScriptPoliciesResponse, error) {
		return nil, context.DeadlineExceeded
	}
	m.poll(context.Background()) // не должен паниковать
	if m.version != 0 || len(m.policies) != 0 {
		t.Fatalf("ошибка fetch не должна менять состояние: version=%d policies=%d", m.version, len(m.policies))
	}
	noCall(t, fr)
}
