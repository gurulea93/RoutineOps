package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// recorder — диспетчер с управляемым поведением для тестов.
type recorder struct {
	mu   sync.Mutex
	got  []string // доставленные payload-ы (в порядке доставки)
	fail bool     // true → имитируем временный сбой связи
}

func (r *recorder) dispatch(_ context.Context, _ string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("связи нет")
	}
	r.got = append(r.got, string(data))
	return nil
}

func (r *recorder) delivered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func newQ(t *testing.T, rec *recorder, max int) *Queue {
	t.Helper()
	q, err := New(t.TempDir(), max, time.Hour, discardLog(), rec.dispatch)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q
}

func TestFlushDeliversFIFO(t *testing.T) {
	rec := &recorder{}
	q := newQ(t, rec, 0)
	for _, s := range []string{"a", "b", "c"} {
		if err := q.Enqueue(KindSecurity, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	q.flush(context.Background())

	if q.Len() != 0 {
		t.Fatalf("очередь не пуста после успешного слива: %d", q.Len())
	}
	got := rec.delivered()
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("порядок доставки FIFO нарушен: got %v want %v", got, want)
	}
}

func TestRetainsOrderOnFailure(t *testing.T) {
	rec := &recorder{fail: true}
	q := newQ(t, rec, 0)
	for _, s := range []string{"a", "b", "c"} {
		_ = q.Enqueue(KindAdmin, []byte(s))
	}
	q.flush(context.Background()) // связь лежит — ничего не доставлено
	if len(rec.delivered()) != 0 {
		t.Fatalf("доставка при сбое: %v", rec.delivered())
	}
	if q.Len() != 3 {
		t.Fatalf("записи потеряны при сбое: %d", q.Len())
	}

	rec.fail = false // связь восстановилась
	q.flush(context.Background())
	got := rec.delivered()
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("после восстановления порядок нарушен: %v", got)
	}
	if q.Len() != 0 {
		t.Fatalf("очередь не очищена: %d", q.Len())
	}
}

// TestPersistsAcrossRestart: записи, не доставленные первым экземпляром, видит
// новый экземпляр поверх того же каталога (агент перезапустился).
func TestPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	rec1 := &recorder{fail: true}
	q1, err := New(dir, 0, time.Hour, discardLog(), rec1.dispatch)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"x", "y"} {
		_ = q1.Enqueue(KindSecurity, []byte(s))
	}
	q1.flush(context.Background()) // не доставлено

	rec2 := &recorder{}
	q2, err := New(dir, 0, time.Hour, discardLog(), rec2.dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Len() != 2 {
		t.Fatalf("новый экземпляр не увидел очередь: %d", q2.Len())
	}
	q2.flush(context.Background())
	got := rec2.delivered()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("после перезапуска доставка неверна: %v", got)
	}
}

func TestEnforceLimitDropsOldest(t *testing.T) {
	rec := &recorder{}
	q := newQ(t, rec, 3)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		if err := q.Enqueue(KindSecurity, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 3 {
		t.Fatalf("лимит не соблюдён: %d (ждали 3)", q.Len())
	}
	q.flush(context.Background())
	got := rec.delivered()
	// Остаться должны самые свежие: 3,4,5.
	if len(got) != 3 || got[0] != "3" || got[2] != "5" {
		t.Fatalf("отброшены не самые старые: %v", got)
	}
}

// TestCorruptedEntryDropped: битый файл не блокирует очередь (poison pill).
func TestCorruptedEntryDropped(t *testing.T) {
	rec := &recorder{}
	q := newQ(t, rec, 0)
	_ = q.Enqueue(KindSecurity, []byte("good"))
	// Подкидываем битый .json напрямую.
	bad := q.dir + "/00000000000000000001-000000000001-security.json"
	if err := writeFile(bad, "{not json"); err != nil {
		t.Fatal(err)
	}
	q.flush(context.Background())
	if q.Len() != 0 {
		t.Fatalf("битая запись заблокировала очередь: %d", q.Len())
	}
	if got := rec.delivered(); len(got) != 1 || got[0] != "good" {
		t.Fatalf("валидная запись не доставлена: %v", got)
	}
}
