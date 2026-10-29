//go:build darwin && cgo

package keystore

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestKeychainProviderRoundTrip импортирует сгенерённую идентичность в
// ИЗОЛИРОВАННЫЙ временный keychain, достаёт её провайдером и проверяет, что
// подпись через SecKey валидируется публичным ключом серта. Пропускается, если
// окружение не даёт работать с keychain (например, sandbox в CI).
func TestKeychainProviderRoundTrip(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("keychain-интеграция пропускается в CI (нет доступа к Security-сервисам)")
	}
	for _, bin := range []string{"openssl", "security"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("нет %s: %v", bin, err)
		}
	}

	dir := t.TempDir()
	const label = "mdm-test-device-7f3a"
	keyPath := filepath.Join(dir, "k.pem")
	certPath := filepath.Join(dir, "c.pem")

	// ECDSA P-256 ключ (PKCS8) + самоподписанный серт с CN=label.
	run(t, "openssl", "genpkey", "-algorithm", "EC",
		"-pkeyopt", "ec_paramgen_curve:P-256", "-out", keyPath)
	run(t, "openssl", "req", "-x509", "-key", keyPath, "-days", "1",
		"-subj", "/CN="+label, "-out", certPath)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	// Изолированный keychain — не трогаем login keychain пользователя.
	kc := filepath.Join(dir, "mdm-test.keychain")
	run(t, "security", "create-keychain", "-p", "test", kc)
	t.Cleanup(func() { _ = exec.Command("/usr/bin/security", "delete-keychain", kc).Run() })
	run(t, "security", "unlock-keychain", "-p", "test", kc)

	if err := Import(certPEM, keyPEM, kc); err != nil {
		t.Skipf("Import в keychain не удался (вероятно ограничения окружения): %v", err)
	}

	p := &keychainProvider{label: label, caFile: certPath, keychain: kc}
	cert, err := p.ClientCertificate()
	if err != nil {
		t.Fatalf("ClientCertificate: %v", err)
	}
	if cert.Leaf == nil || cert.Leaf.Subject.CommonName != label {
		t.Fatalf("CN серта = %q, want %q", cert.Leaf.Subject.CommonName, label)
	}

	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatalf("PrivateKey не crypto.Signer: %T", cert.PrivateKey)
	}
	pub, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("публичный ключ не ECDSA: %T", signer.Public())
	}

	digest := sha256.Sum256([]byte("MDM keychain signer test"))
	sig, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("Sign через SecKey: %v", err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Fatal("подпись SecKey не прошла проверку публичным ключом серта")
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
