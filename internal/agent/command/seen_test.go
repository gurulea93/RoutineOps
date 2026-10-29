package command

import (
	"path/filepath"
	"testing"
)

func TestSeenInMemory(t *testing.T) {
	s := loadSeenSet("")
	if !s.markIfNew("a") {
		t.Fatal("первый раз id должен быть новым")
	}
	if s.markIfNew("a") {
		t.Fatal("повторный id должен считаться дубликатом")
	}
	if !s.markIfNew("b") {
		t.Fatal("другой id должен быть новым")
	}
}

// TestSeenPersistence проверяет, что идемпотентность переживает рестарт агента.
func TestSeenPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.seen")

	s1 := loadSeenSet(path)
	if !s1.markIfNew("task-1") || !s1.markIfNew("task-2") {
		t.Fatal("новые id должны помечаться")
	}
	if s1.markIfNew("task-1") {
		t.Fatal("task-1 уже виден")
	}

	// Имитация рестарта: новый набор из того же файла.
	s2 := loadSeenSet(path)
	if s2.markIfNew("task-1") {
		t.Fatal("task-1 должен помниться после рестарта")
	}
	if s2.markIfNew("task-2") {
		t.Fatal("task-2 должен помниться после рестарта")
	}
	if !s2.markIfNew("task-3") {
		t.Fatal("task-3 новый — должен пройти")
	}
}
