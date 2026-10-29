package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nonComment возвращает строки файла без комментариев/пустых — то, что увидит
// Security Monitor (security.loadForbidden парсит так же).
func nonComment(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestWriteListRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.txt")
	if err := writeList(p, 1700000000, []string{"sleep", "Foo"}); err != nil {
		t.Fatal(err)
	}
	if v := readVersion(p); v != 1700000000 {
		t.Errorf("readVersion=%d want 1700000000", v)
	}
	got := nonComment(t, p)
	if len(got) != 2 || got[0] != "sleep" || got[1] != "Foo" {
		t.Errorf("список запрещённого=%v want [sleep Foo]", got)
	}
}

func TestWriteListEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.txt")
	if err := writeList(p, 42, nil); err != nil {
		t.Fatal(err)
	}
	if v := readVersion(p); v != 42 {
		t.Errorf("readVersion=%d want 42", v)
	}
	if got := nonComment(t, p); len(got) != 0 {
		t.Errorf("ожидали пустой список, got=%v", got)
	}
}

func TestReadVersionMissing(t *testing.T) {
	if v := readVersion(filepath.Join(t.TempDir(), "none.txt")); v != 0 {
		t.Errorf("readVersion отсутствующего файла=%d want 0", v)
	}
}
