package lock

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

// newMgrDurable — Manager с durable-памятью снятия в отдельном («защищённом»)
// каталоге, как в бою (SetDurableUnlockPath до Load).
func newMgrDurable(t *testing.T, l Locker) (*Manager, string) {
	t.Helper()
	durable := filepath.Join(t.TempDir(), "lock.last_unlocked")
	m := New(filepath.Join(t.TempDir(), "lock.json"), l, quietLog())
	m.SetDurableUnlockPath(durable)
	return m, durable
}

// #7, сценарий атаки: обычный пользователь (Windows: каталог lock.json
// user-writable по замыслу) копирует hash активного лока в last_unlocked_hash
// и пишет {"locked":false,...} при остановленной службе. После старта Load
// ОБЯЗАН игнорировать значение из user-writable файла (durable-память — только
// защищённый файл, которого у атакующего нет), а реконсиляция — пере-запереть
// устройство, как это делало до-батчевое самолечение.
func TestLoad_ForgedLockJSONDoesNotSuppressRelock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.json")
	hash := bcryptHash(t, "pw")
	forged, err := json.Marshal(State{Locked: false, LastUnlockedHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, forged, 0o644); err != nil {
		t.Fatal(err)
	}

	fl := &fakeLocker{}
	m := New(path, fl, quietLog())
	m.SetDurableUnlockPath(filepath.Join(t.TempDir(), "lock.last_unlocked")) // защищённый файл отсутствует
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if got := m.LastUnlockedHash(); got != "" {
		t.Fatalf("LastUnlockedHash=%q взят из подделанного user-writable lock.json — durable-подавление подделываемо (#7)", got)
	}

	r := newTestReconciler(m, &pb.FetchLockStatusResponse{Locked: true, PasswordHash: hash}, nil)
	r.tick(context.Background())
	if !m.Locked() {
		t.Fatal("устройство не пере-заперто по desired=locked после подделки файла — kill-switch подавлен молча")
	}
}

// Durable-память переживает «ребут» через ЗАЩИЩЁННЫЙ файл (#4), а маркер в
// user-writable lock.json при Load игнорируется даже когда подсунут чужой (#7).
func TestDurableUnlockMemory_SurvivesRestartFromProtectedFile(t *testing.T) {
	durable := filepath.Join(t.TempDir(), "lock.last_unlocked")
	lockPath := filepath.Join(t.TempDir(), "lock.json")
	hash := bcryptHash(t, "pw")

	m := New(lockPath, &fakeLocker{}, quietLog())
	m.SetDurableUnlockPath(durable)
	if err := m.Lock("r1", hash, "увольнение"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := m.Unlock(); err != nil { // локальное снятие → durable-запись в защищённый файл
		t.Fatalf("Unlock: %v", err)
	}
	if b, err := os.ReadFile(durable); err != nil || string(b) != hash {
		t.Fatalf("durable-файл = %q (err=%v), ожидали hash снятого лока", b, err)
	}

	// Атакующий дополнительно подсунул чужой маркер в user-writable lock.json —
	// он не должен ни подавлять, ни подменять durable-значение.
	forged, _ := json.Marshal(State{Locked: false, LastUnlockedHash: bcryptHash(t, "attacker")})
	if err := os.WriteFile(lockPath, forged, 0o644); err != nil {
		t.Fatal(err)
	}

	// «Ребут»: новый Manager с тем же защищённым файлом.
	restarted := New(lockPath, &fakeLocker{}, quietLog())
	restarted.SetDurableUnlockPath(durable)
	if err := restarted.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if restarted.Locked() {
		t.Fatal("после локального снятия устройство не должно быть заблокировано после рестарта")
	}
	if got := restarted.LastUnlockedHash(); got != hash {
		t.Fatalf("durable LastUnlockedHash после рестарта = %q, ожидали %q (не из подделанного lock.json)", got, hash)
	}
}

// ClearLastUnlocked забывает durable-память (сервер подтвердил unlocked): файл
// удаляется, память пуста — следующий Lock/desired-locked снова сработает.
func TestClearLastUnlocked_RemovesProtectedFile(t *testing.T) {
	m, durable := newMgrDurable(t, &fakeLocker{})
	hash := bcryptHash(t, "pw")
	if err := m.Lock("r1", hash, "reason"); err != nil {
		t.Fatal(err)
	}
	if err := m.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(durable); err != nil {
		t.Fatalf("durable-файл должен существовать после Unlock: %v", err)
	}

	m.ClearLastUnlocked()
	if got := m.LastUnlockedHash(); got != "" {
		t.Fatalf("LastUnlockedHash после ClearLastUnlocked = %q, ожидали пусто", got)
	}
	if _, err := os.Stat(durable); !os.IsNotExist(err) {
		t.Fatalf("durable-файл не удалён после ClearLastUnlocked: err=%v", err)
	}
}
