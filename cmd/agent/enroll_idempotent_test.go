package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/config"
)

// writeIdentity пишет самоподписанную пару cert/key (CN=cn, срок задаётся
// notBefore/notAfter) во временный каталог и возвращает пути.
func writeIdentity(t *testing.T, cn string, notBefore, notAfter time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "agent.crt")
	keyFile = filepath.Join(dir, "agent.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// TestExistingDeviceID проверяет идемпотентный гейт энроллмента: валидный серт →
// (CN, true) и enroll не повторяется; нет серта / истёкший → ("", false) и enroll
// выполняется (иначе повторный токен дал бы 401 → откат всей установки, баг v22).
func TestExistingDeviceID(t *testing.T) {
	now := time.Now()

	t.Run("валидный серт → пропуск энроллмента", func(t *testing.T) {
		cert, key := writeIdentity(t, "device-abc123", now.Add(-time.Hour), now.Add(24*time.Hour))
		cfg := &config.Config{CertSource: "file", CertFile: cert, KeyFile: key}
		id, ok := existingDeviceID(cfg, now)
		if !ok || id != "device-abc123" {
			t.Fatalf("existingDeviceID = (%q, %v), хотим (device-abc123, true)", id, ok)
		}
	})

	t.Run("истёкший серт → энроллмент нужен", func(t *testing.T) {
		cert, key := writeIdentity(t, "device-old", now.Add(-48*time.Hour), now.Add(-time.Hour))
		cfg := &config.Config{CertSource: "file", CertFile: cert, KeyFile: key}
		if id, ok := existingDeviceID(cfg, now); ok {
			t.Fatalf("истёкший серт принят за валидный: (%q, %v)", id, ok)
		}
	})

	t.Run("нет серта на диске → энроллмент нужен", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			CertSource: "file",
			CertFile:   filepath.Join(dir, "missing.crt"),
			KeyFile:    filepath.Join(dir, "missing.key"),
		}
		if id, ok := existingDeviceID(cfg, now); ok {
			t.Fatalf("отсутствующий серт принят за валидный: (%q, %v)", id, ok)
		}
	})

	t.Run("серт без CN → энроллмент нужен", func(t *testing.T) {
		cert, key := writeIdentity(t, "", now.Add(-time.Hour), now.Add(24*time.Hour))
		cfg := &config.Config{CertSource: "file", CertFile: cert, KeyFile: key}
		if id, ok := existingDeviceID(cfg, now); ok {
			t.Fatalf("серт без CN принят за валидный: (%q, %v)", id, ok)
		}
	})
}
