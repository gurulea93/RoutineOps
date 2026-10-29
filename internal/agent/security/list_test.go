package security

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadForbidden(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	content := "# комментарий\nFooBar\n\n   BAZ  \n# ещё\nqux\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadForbidden(p)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := []string{"foobar", "baz", "qux"} // нижний регистр, trim, без комментов/пустых
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

func TestLoadForbiddenMissingFile(t *testing.T) {
	got, err := loadForbidden(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("отсутствие файла не должно быть ошибкой: %v", err)
	}
	if got != nil {
		t.Errorf("ожидали пустой список, got=%v", got)
	}
}
