package security

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/collector"
	"github.com/Floodww/RoutineOps/internal/agent/outbox"
	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/protobuf/proto"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeList создаёт файл со списком запрещённого ПО (по шаблону на строку).
func writeList(t *testing.T, patterns ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forbidden.txt")
	data := ""
	for _, p := range patterns {
		data += p + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

type capturedEvent struct {
	kind string
	data []byte
}

// sink потокобезопасно собирает поставленные в очередь события (Run шлёт их из
// фоновой горутины).
type sink struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (s *sink) enqueue(kind string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, capturedEvent{kind: kind, data: data})
	return nil
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *sink) at(i int) capturedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[i]
}

// newTestMonitor собирает Monitor с подставным списком процессов и захватом
// поставленных в очередь событий. Инвентарь ПО по умолчанию пуст (и НЕ ходит в
// реальный collector.InstalledSoftware — на macOS это system_profiler, секунды).
func newTestMonitor(t *testing.T, listFile string, procs func() ([]Process, error)) (*Monitor, *sink) {
	t.Helper()
	s := &sink{}
	m := NewMonitor(time.Hour, listFile, s.enqueue, quietLog())
	m.listProcs = procs
	m.listInstalled = func() []collector.Software { return nil }
	return m, s
}

// report сериализует SecurityEvent и ставит его в очередь под видом KindSecurity.
func TestReport_EnqueuesSecurityEvent(t *testing.T) {
	m, captured := newTestMonitor(t, "", nil)
	ok := m.report("malware", `запрещённое ПО запущено: "malware" (процесс malware, pid 42)`)
	if !ok {
		t.Fatal("report вернул false при успешной постановке")
	}
	if captured.count() != 1 {
		t.Fatalf("ожидалось 1 событие, получено %d", captured.count())
	}
	ev := captured.at(0)
	if ev.kind != outbox.KindSecurity {
		t.Errorf("kind = %q, ожидался %q", ev.kind, outbox.KindSecurity)
	}
	var se pb.SecurityEvent
	if err := proto.Unmarshal(ev.data, &se); err != nil {
		t.Fatalf("Unmarshal SecurityEvent: %v", err)
	}
	if se.AlertType != pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE {
		t.Errorf("AlertType = %v", se.AlertType)
	}
	if se.OccurredAt == 0 {
		t.Error("OccurredAt не проставлен")
	}
}

// Ошибка постановки в очередь → report возвращает false.
func TestReport_EnqueueError_ReturnsFalse(t *testing.T) {
	m := NewMonitor(time.Hour, "", func(string, []byte) error {
		return errors.New("queue full")
	}, quietLog())
	if m.report("x", "запрещённое ПО запущено: x") {
		t.Error("report вернул true несмотря на ошибку Enqueue")
	}
}

// Ключевая логика: алерт ставится один раз на эпизод; после исчезновения
// процесса отметка снимается, повторный запуск даёт новый алерт.
func TestScan_AlertsOncePerEpisode(t *testing.T) {
	listFile := writeList(t, "evil")
	running := true
	procs := func() ([]Process, error) {
		if running {
			return []Process{{PID: 7, Name: "evil", Cmd: "/tmp/evil"}}, nil
		}
		return []Process{{PID: 1, Name: "bash", Cmd: "/bin/bash"}}, nil
	}
	m, captured := newTestMonitor(t, listFile, procs)
	ctx := context.Background()

	m.scan(ctx) // первое срабатывание → алерт
	m.scan(ctx) // тот же процесс → без нового алерта
	if captured.count() != 1 {
		t.Fatalf("после двух сканов одного процесса ожидался 1 алерт, получено %d", captured.count())
	}

	running = false
	m.scan(ctx) // процесс исчез → отметка снимается, алертов не добавляется
	if captured.count() != 1 {
		t.Fatalf("исчезновение процесса не должно слать алерт, получено %d", captured.count())
	}

	running = true
	m.scan(ctx) // запущен заново → новый эпизод → новый алерт
	if captured.count() != 2 {
		t.Fatalf("повторный запуск должен дать новый алерт, всего %d", captured.count())
	}
}

// details извлекает поле Details из захваченного SecurityEvent.
func details(t *testing.T, ev capturedEvent) string {
	t.Helper()
	var se pb.SecurityEvent
	if err := proto.Unmarshal(ev.data, &se); err != nil {
		t.Fatalf("Unmarshal SecurityEvent: %v", err)
	}
	return se.Details
}

// noProcs — подставной пустой список процессов.
func noProcs() ([]Process, error) { return nil, nil }

// БАГ «алерт не рождается для установленного, но не запущенного ПО»: правило из
// инвентарного пространства имён («Google Chrome» — DisplayName) не совпадает с
// именем процесса (chrome.exe) — сервер показывал нарушение compliance, а алерта
// не было НИКОГДА. Теперь инвентарный матч даёт алерт; эпизод снимается только
// после удаления ПО, повторная установка — новый алерт.
func TestScan_InstalledForbiddenAlerts(t *testing.T) {
	listFile := writeList(t, "google chrome")
	installed := []collector.Software{{Name: "Google Chrome", Version: "126.0"}}
	m, captured := newTestMonitor(t, listFile, noProcs)
	m.installedRefresh = 0 // рефреш на каждый скан — тест управляет снимком напрямую
	m.listInstalled = func() []collector.Software { return installed }
	ctx := context.Background()

	m.scan(ctx) // установлено → алерт
	m.scan(ctx) // всё ещё установлено → без нового алерта
	if captured.count() != 1 {
		t.Fatalf("после двух сканов установленного ПО ожидался 1 алерт, получено %d", captured.count())
	}
	if d := details(t, captured.at(0)); !strings.Contains(d, "установлено") ||
		!strings.Contains(d, "Google Chrome") || !strings.Contains(d, "126.0") {
		t.Errorf("детали инвентарного алерта неполные: %q", d)
	}

	installed = nil
	m.scan(ctx) // ПО удалено → отметка снимается, алертов не добавляется
	installed = []collector.Software{{Name: "Google Chrome", Version: "127.0"}}
	m.scan(ctx) // установлено заново → новый эпизод → новый алерт
	if captured.count() != 2 {
		t.Fatalf("переустановка должна дать новый алерт, всего %d", captured.count())
	}
}

// Один шаблон виден и как процесс, и в инвентаре → ровно один алерт, и он
// runtime-приоритетный («запущено», с pid).
func TestScan_RunningPreferredOverInstalled(t *testing.T) {
	listFile := writeList(t, "chrome")
	procs := func() ([]Process, error) {
		return []Process{{PID: 9, Name: "chrome.exe", Cmd: `C:\Program Files\Google\Chrome\chrome.exe`}}, nil
	}
	m, captured := newTestMonitor(t, listFile, procs)
	m.installedRefresh = 0
	m.listInstalled = func() []collector.Software {
		return []collector.Software{{Name: "Google Chrome", Version: "126.0"}}
	}
	m.scan(context.Background())
	if captured.count() != 1 {
		t.Fatalf("ожидался ровно 1 алерт, получено %d", captured.count())
	}
	if d := details(t, captured.at(0)); !strings.Contains(d, "запущено") {
		t.Errorf("при живом процессе алерт должен быть runtime («запущено»): %q", d)
	}
}

// Рестарт агента НЕ должен рождать дубль-алерт для всё ещё установленного ПО:
// сервер алерты не дедуплицирует (каждый = строка в БД + Telegram), а без
// персиста эпизодов каждый self-update/ребут флота давал бы всплеск дублей.
func TestScan_AlertedPersistsAcrossRestart(t *testing.T) {
	listFile := writeList(t, "google chrome")
	installed := func() []collector.Software {
		return []collector.Software{{Name: "Google Chrome", Version: "126.0"}}
	}
	ctx := context.Background()

	m1, captured1 := newTestMonitor(t, listFile, noProcs)
	m1.installedRefresh = 0
	m1.listInstalled = installed
	m1.scan(ctx)
	if captured1.count() != 1 {
		t.Fatalf("первый агент: ожидался 1 алерт, получено %d", captured1.count())
	}

	// «Рестарт»: новый Monitor с тем же ListFile (state-файл рядом с ним).
	m2, captured2 := newTestMonitor(t, listFile, noProcs)
	m2.installedRefresh = 0
	m2.listInstalled = installed
	m2.scan(ctx)
	if captured2.count() != 0 {
		t.Fatalf("после рестарта дубль-алерта быть не должно, получено %d", captured2.count())
	}

	// ПО удалили, пока агент «не работал» → эпизод закрывается, переустановка
	// после ещё одного «рестарта» — новый алерт.
	m3, captured3 := newTestMonitor(t, listFile, noProcs)
	m3.installedRefresh = 0
	empty := true
	m3.listInstalled = func() []collector.Software {
		if empty {
			return nil
		}
		return installed()
	}
	m3.scan(ctx) // удалено → зачистка эпизода + сохранение
	empty = false
	m3.scan(ctx) // установлено заново → новый алерт
	if captured3.count() != 1 {
		t.Fatalf("переустановка после рестарта должна дать 1 алерт, получено %d", captured3.count())
	}
}

// Инвентарный вызов дорогой (macOS: system_profiler) — между сканами снимок
// кэшируется и обновляется не чаще installedRefresh.
func TestScan_InstalledSnapshotCached(t *testing.T) {
	listFile := writeList(t, "нечто-несуществующее")
	m, _ := newTestMonitor(t, listFile, noProcs)
	calls := 0
	m.listInstalled = func() []collector.Software { calls++; return nil }
	m.installedRefresh = time.Hour
	ctx := context.Background()

	m.scan(ctx)
	m.scan(ctx)
	m.scan(ctx)
	if calls != 1 {
		t.Fatalf("снимок ПО должен кэшироваться: вызовов=%d, хотим 1", calls)
	}
}

// Пустой список запрещённого ПО → процессы не перечисляются, алертов нет.
func TestScan_EmptyList_NoAlert(t *testing.T) {
	listFile := writeList(t) // пустой файл
	called := false
	procs := func() ([]Process, error) {
		called = true
		return []Process{{PID: 1, Name: "evil"}}, nil
	}
	m, captured := newTestMonitor(t, listFile, procs)
	m.scan(context.Background())
	if called {
		t.Error("при пустом списке процессы не должны перечисляться")
	}
	if captured.count() != 0 {
		t.Errorf("ожидалось 0 алертов, получено %d", captured.count())
	}
}

// Ошибка перечисления процессов не должна паниковать и не шлёт алерт.
func TestScan_ListProcsError_NoAlert(t *testing.T) {
	listFile := writeList(t, "evil")
	procs := func() ([]Process, error) {
		return nil, errors.New("ps failed")
	}
	m, captured := newTestMonitor(t, listFile, procs)
	m.scan(context.Background())
	if captured.count() != 0 {
		t.Errorf("при ошибке перечисления алертов быть не должно, получено %d", captured.count())
	}
}

// Run должен выполнить хотя бы один scan сразу и завершиться по отмене контекста.
func TestRun_ScansImmediatelyAndStops(t *testing.T) {
	listFile := writeList(t, "evil")
	procs := func() ([]Process, error) {
		return []Process{{PID: 7, Name: "evil", Cmd: "/tmp/evil"}}, nil
	}
	m, captured := newTestMonitor(t, listFile, procs)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	// Первый scan выполняется до тика — ждём появления алерта.
	deadline := time.After(2 * time.Second)
	for captured.count() == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("Run не выполнил первый scan вовремя")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился после отмены контекста")
	}
}
