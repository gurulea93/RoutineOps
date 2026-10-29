//go:build !windows

package lock

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureUserWritableDir_StickyWorldWritable — регрессия на полевой баг v1.5.2:
// каталог должен быть доступен на запись не только владельцу (демон под root), но и
// любому локальному пользователю (лок-экран/трей юзер-сессии без прав root), иначе
// ClearState при верном пароле тихо падает и SessionLocker поднимает оверлей заново.
func TestEnsureUserWritableDir_StickyWorldWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")

	if err := EnsureUserWritableDir(dir); err != nil {
		t.Fatalf("EnsureUserWritableDir: %v", err)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o777 {
		t.Errorf("права каталога = %v, хотим 0o777 (запись всем)", fi.Mode().Perm())
	}
	if fi.Mode()&os.ModeSticky == 0 {
		t.Error("нет sticky-бита — другой пользователь сможет удалить/переименовать чужие файлы")
	}

	// Идемпотентность: повторный вызов на уже существующий каталог не должен падать
	// и должен сохранить режим (например, если каталог остался от версии до фикса,
	// где ставился 0o755).
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := EnsureUserWritableDir(dir); err != nil {
		t.Fatalf("EnsureUserWritableDir (повторно): %v", err)
	}
	fi, err = os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat (повторно): %v", err)
	}
	if fi.Mode().Perm() != 0o777 || fi.Mode()&os.ModeSticky == 0 {
		t.Errorf("повторный вызов не довозвёл права: режим = %v", fi.Mode())
	}
}
