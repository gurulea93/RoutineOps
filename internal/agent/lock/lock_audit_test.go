package lock

import (
	"path/filepath"
	"testing"
)

// #13: Lock отвергает пустой/не-bcrypt password_hash (fail-safe против
// офлайн-неснимаемого лока) и НЕ поднимает замок.
func TestLock_RejectsInvalidHash(t *testing.T) {
	for _, tc := range []struct {
		name string
		hash string
	}{
		{"пустой", ""},
		{"не bcrypt", "just-a-string"},
		{"обрезанный bcrypt", "$2a$10$short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fl := &fakeLocker{}
			m := newMgr(t, fl)
			if err := m.Lock("r1", tc.hash, "причина"); err == nil {
				t.Fatal("Lock принял невалидный хеш — риск офлайн-неснимаемого лока")
			}
			if m.Locked() {
				t.Fatal("устройство заблокировано невалидным хешем (не fail-safe)")
			}
			if fl.shows != 0 {
				t.Fatalf("замок поднят при невалидном хеше, shows=%d", fl.shows)
			}
		})
	}
}

// #4: hash локально снятого лока переживает «ребут» (новый Manager) через
// durable-память в ЗАЩИЩЁННОМ файле — чтобы реконсиляция не пере-заперла по
// устаревшему desired до доставки UNLOCKED-отчёта. Durable-память живёт НЕ в
// user-writable lock.json (её оттуда подделали бы, #7), а в отдельном файле
// защищённого каталога состояния (SetDurableUnlockPath).
func TestUnlock_PersistsLastUnlockedHashAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.json")
	durable := filepath.Join(t.TempDir(), "lock.last_unlocked")
	hash := bcryptHash(t, "pw")

	m := New(path, &fakeLocker{}, quietLog())
	m.SetDurableUnlockPath(durable)
	if err := m.Lock("r1", hash, "увольнение"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := m.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if m.LastUnlockedHash() != hash {
		t.Fatalf("LastUnlockedHash в памяти = %q, want %q", m.LastUnlockedHash(), hash)
	}

	// «Ребут»: новый Manager с тем же защищённым файлом.
	restarted := New(path, &fakeLocker{}, quietLog())
	restarted.SetDurableUnlockPath(durable)
	if err := restarted.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if restarted.Locked() {
		t.Fatal("после локального снятия устройство не должно быть заблокировано после рестарта")
	}
	if restarted.LastUnlockedHash() != hash {
		t.Fatalf("durable LastUnlockedHash потерян при рестарте: %q, want %q", restarted.LastUnlockedHash(), hash)
	}
}
