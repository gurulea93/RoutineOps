package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeEntryAt кладёт в каталог очереди валидную запись с заданным временем
// постановки (кодируется в префиксе имени файла, как делает newName).
func writeEntryAt(t *testing.T, dir, kind, payload string, at time.Time) {
	t.Helper()
	buf, err := json.Marshal(entry{Kind: kind, Data: []byte(payload), EnqueuedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%019d-%012d-%s.json", at.UnixNano(), 1, sanitize(kind))
	if err := os.WriteFile(filepath.Join(dir, name), buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

// При включённом maxAge устаревшие записи дропаются на flush, свежие остаются и
// доставляются.
func TestEnforceAgeDropsStale(t *testing.T) {
	rec := &recorder{}
	q := newQ(t, rec, 0)
	q.SetMaxAge(time.Hour)

	writeEntryAt(t, q.dir, KindSecurity, "old", time.Now().Add(-2*time.Hour)) // старше maxAge
	writeEntryAt(t, q.dir, KindSecurity, "fresh", time.Now().Add(-time.Minute))

	q.flush(context.Background())

	got := rec.delivered()
	if len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("ожидали доставку только свежей записи, got %v", got)
	}
	if q.Len() != 0 {
		t.Fatalf("очередь не пуста после слива: %d", q.Len())
	}
}

// Ретеншен по возрасту работает даже когда связи нет (flush выходит на первом
// сбое доставки, но устаревший backlog всё равно подрезается).
func TestEnforceAgeWhenOffline(t *testing.T) {
	rec := &recorder{fail: true}
	q := newQ(t, rec, 0)
	q.SetMaxAge(time.Hour)

	writeEntryAt(t, q.dir, KindAdmin, "old", time.Now().Add(-3*time.Hour))
	writeEntryAt(t, q.dir, KindAdmin, "recent", time.Now())

	q.flush(context.Background()) // связи нет: ничего не доставлено
	if len(rec.delivered()) != 0 {
		t.Fatalf("при сбое связи не должно быть доставки: %v", rec.delivered())
	}
	// Старая запись выметена ретеншеном, недоставленная свежая осталась.
	if q.Len() != 1 {
		t.Fatalf("ожидали 1 запись после ретеншена, got %d", q.Len())
	}
}

// maxAge=0 (по умолчанию) — ретеншен по возрасту выключен, древние записи живут.
func TestMaxAgeZeroKeepsAll(t *testing.T) {
	rec := &recorder{fail: true}
	q := newQ(t, rec, 0) // SetMaxAge не вызывали → maxAge==0

	writeEntryAt(t, q.dir, KindSecurity, "ancient", time.Now().Add(-1000*time.Hour))
	q.flush(context.Background())

	if q.Len() != 1 {
		t.Fatalf("при maxAge=0 запись не должна дропаться по возрасту: %d", q.Len())
	}
}

// fileTime разбирает префикс UnixNano и отвергает мусорные имена.
func TestFileTime(t *testing.T) {
	now := time.Now()
	name := fmt.Sprintf("%019d-%012d-%s.json", now.UnixNano(), 7, "security")
	ts, ok := fileTime(name)
	if !ok {
		t.Fatal("ожидали успешный разбор валидного имени")
	}
	if ts.UnixNano() != now.UnixNano() {
		t.Fatalf("неверное время: got %d want %d", ts.UnixNano(), now.UnixNano())
	}

	for _, bad := range []string{"", "nodash.json", "-123-x.json", "notanumber-1-x.json"} {
		if _, ok := fileTime(bad); ok {
			t.Errorf("ожидали отказ разбора для %q", bad)
		}
	}
}
