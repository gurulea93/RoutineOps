//go:build windows

package keystore

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ProvisionTarget — стор по умолчанию для раскладки идентичности при enroll.
// Под повышенными правами (служба-агент работает как LocalSystem) — LocalMachine\My;
// иначе CurrentUser\My. Провайдер ищет в LocalMachine, затем CurrentUser.
func ProvisionTarget() string {
	if windows.GetCurrentProcessToken().IsElevated() {
		return "LocalMachine"
	}
	return "CurrentUser"
}

// ProvisionIsMachineScope сообщает, попадёт ли идентичность в машинное хранилище
// (LocalMachine\My), доступное службе под LocalSystem. false → пользовательский
// стор, который служба не увидит. См. runEnroll: гвард для keystore+install-service.
func ProvisionIsMachineScope() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// Import кладёт выданную при энроллменте идентичность (cert + приватный ключ) в
// Windows Certificate Store. Прямого импорта PEM в стор нет, поэтому собираем
// PFX в памяти (go-pkcs12) и импортируем штатным `certutil -importpfx` (есть на
// любой Windows, CGO не нужен). target: "LocalMachine" | "CurrentUser".
// NoExport помечает ключ неэкспортируемым — секрет остаётся в сторе.
func Import(certPEM, keyPEM []byte, target string) error {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return err
	}
	key, err := parsePrivateKeyPEM(keyPEM)
	if err != nil {
		return err
	}

	// Транзитный пароль PFX — нужен только на время передачи в certutil.
	pwRaw := make([]byte, 16)
	if _, err := rand.Read(pwRaw); err != nil {
		return err
	}
	password := hex.EncodeToString(pwRaw)

	pfx, err := pkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		return fmt.Errorf("сборка PFX: %w", err)
	}

	dir, err := os.MkdirTemp("", "mdm-enroll-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pfxPath := filepath.Join(dir, "identity.pfx")
	if err := os.WriteFile(pfxPath, pfx, 0o600); err != nil {
		return err
	}

	args := []string{"-f", "-p", password}
	if target == "CurrentUser" {
		args = append(args, "-user")
	}
	args = append(args, "-importpfx", "My", pfxPath, "NoExport")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "certutil", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("certutil -importpfx (%s): %w: %s", target, err, out)
	}
	return nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("сертификат не PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parsePrivateKeyPEM(keyPEM []byte) (interface{}, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("приватный ключ не PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("неподдержанный формат приватного ключа")
}
