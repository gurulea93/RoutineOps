package enroll

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadCABundlePin проверяет пин CA-бандла по sha256: совпадение проходит,
// несовпадение (имитация подмены MITM) — отвергается, без пина — пропускается.
func TestLoadCABundlePin(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	bundle := []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n")
	if err := os.WriteFile(caPath, bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	good := hex.EncodeToString(sum[:])

	t.Run("совпадение пина", func(t *testing.T) {
		b, err := loadCABundle(caPath, "", good)
		if err != nil {
			t.Fatalf("верный пин должен пройти: %v", err)
		}
		if string(b) != string(bundle) {
			t.Fatal("вернулся не тот бандл")
		}
	})

	t.Run("регистр пина не важен", func(t *testing.T) {
		if _, err := loadCABundle(caPath, "", strings.ToUpper(good)); err != nil {
			t.Fatalf("пин должен сверяться без учёта регистра: %v", err)
		}
	})

	t.Run("несовпадение пина отвергается", func(t *testing.T) {
		bad := strings.Repeat("0", 64)
		if _, err := loadCABundle(caPath, "", bad); err == nil {
			t.Fatal("неверный пин должен отвергнуть бандл (MITM)")
		}
	})

	t.Run("без пина пропускается", func(t *testing.T) {
		if _, err := loadCABundle(caPath, "", ""); err != nil {
			t.Fatalf("без пина загрузка должна работать как раньше: %v", err)
		}
	})
}

// TestLoadCABundleRefetchesOnStalePin — recovery после ПЕРЕИЗДАНИЯ CA: на диске лежит
// старый бандл, -ca-sha256 уже от нового, -ca-url отдаёт новый. Раньше это был
// терминальный отказ (старый файл не проходил пин, до URL не доходили), и устройство
// залипало без ручного rm. Теперь старый бандл, не сойдясь с пином, уступает свежему.
func TestLoadCABundleRefetchesOnStalePin(t *testing.T) {
	oldCA := newTestCA(t)
	newCA := newTestCA(t)

	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, oldCA.pem, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newCA.pem)
	}))
	defer srv.Close()

	sum := sha256.Sum256(newCA.pem)
	newPin := hex.EncodeToString(sum[:])

	// Файл на диске не сходится с новым пином → тянем свежий по URL.
	got, err := loadCABundle(caPath, srv.URL, newPin)
	if err != nil {
		t.Fatalf("recovery после переиздания CA должен пройти: %v", err)
	}
	if string(got) != string(newCA.pem) {
		t.Fatal("вернулся старый бандл вместо свежего скачанного")
	}

	// Пин не сходится ни с файлом, ни со скачанным → отказ (MITM не проходит).
	if _, err := loadCABundle(caPath, srv.URL, strings.Repeat("a", 64)); err == nil {
		t.Fatal("ни файл, ни URL не сошлись с пином — ожидали отказ")
	}
}
