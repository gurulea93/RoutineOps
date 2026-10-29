package collector

import (
	"os"
	"runtime"
	"testing"
)

// Collect собирает снимок устройства, вызывая платформенные сборщики (read-only:
// sw_vers/sysctl на macOS, /proc и Statfs на Linux). Проверяем, что не паникует
// и заполняет инвариантные поля.
func TestCollectSmoke(t *testing.T) {
	di := Collect()

	if host, _ := os.Hostname(); di.Hostname != host {
		t.Errorf("Hostname=%q, ожидали %q", di.Hostname, host)
	}
	if di.OS != normalizeOS(runtime.GOOS) {
		t.Errorf("OS=%q, ожидали %q", di.OS, normalizeOS(runtime.GOOS))
	}
	if di.RAMMegabytes < 0 {
		t.Errorf("RAMMegabytes отрицательный: %d", di.RAMMegabytes)
	}
	// OSVersion/CPU/Disk зависят от окружения и могут быть пустыми в урезанных
	// контейнерах — здесь важно лишь, что Collect отработал без паники.
}

// InstalledSoftware (платформенный список ПО) — best-effort, не должна паниковать
// и всегда возвращает корректный (возможно пустой) срез.
func TestInstalledSoftwareSmoke(t *testing.T) {
	sw := InstalledSoftware()
	for _, s := range sw {
		if s.Name == "" {
			t.Fatal("в списке ПО есть запись с пустым именем")
		}
	}
}
