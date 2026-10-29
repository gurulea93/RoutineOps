//go:build darwin

package keystore

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ProvisionTarget — keychain по умолчанию для раскладки идентичности при enroll.
// Под root (служба-демон launchd под LocalSystem-эквивалентом) — System keychain,
// доступный без пользовательской сессии; иначе login keychain текущего юзера.
func ProvisionTarget() string {
	if os.Geteuid() == 0 {
		return "/Library/Keychains/System.keychain"
	}
	return ""
}

// ProvisionIsMachineScope сообщает, попадёт ли идентичность в System keychain,
// доступный демону launchd без пользовательской сессии. false → login keychain
// текущего юзера, недоступный демону. См. runEnroll: гвард для keystore+install-service.
func ProvisionIsMachineScope() bool {
	return os.Geteuid() == 0
}

// Import кладёт выданную при энроллменте идентичность (cert + приватный ключ) в
// Keychain через утилиту `security` (CGO не нужен). После импорта идентичность
// доступна провайдеру по метке = CN серта (device_id). keychain — путь к файлу
// keychain ("" = login keychain пользователя/демона).
//
// `-A` разрешает доступ к ключу любому приложению на устройстве: агент работает
// демоном (launchd) без интерактивной сессии, иначе подпись TLS упёрлась бы в
// ACL-промпт. Сам ключ всё равно не покидает Keychain.
func Import(certPEM, keyPEM []byte, keychain string) error {
	// `security import -f openssl` принимает ключ только в традиционном формате
	// (EC/RSA PRIVATE KEY); enroll генерит PKCS8 (PRIVATE KEY) — конвертируем.
	keyPEM, err := toTraditionalKeyPEM(keyPEM)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "mdm-enroll-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	keyPath := filepath.Join(dir, "agent.key.pem")
	certPath := filepath.Join(dir, "agent.crt.pem")
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return err
	}

	// Ключ и сертификат импортируем по отдельности; Keychain связывает их в
	// идентичность по совпадению публичного ключа.
	imports := []struct {
		path  string
		kind  string
		extra []string
	}{
		{keyPath, "priv", []string{"-f", "openssl"}},
		{certPath, "cert", nil},
	}
	for _, imp := range imports {
		args := append([]string{"import", imp.path, "-A", "-t", imp.kind}, imp.extra...)
		if keychain != "" {
			args = append(args, "-k", keychain)
		}
		if out, err := exec.Command("/usr/bin/security", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("security import %s: %w: %s", filepath.Base(imp.path), err, out)
		}
	}
	return nil
}

// toTraditionalKeyPEM приводит приватный ключ к формату, который понимает
// `security import -f openssl` (EC PRIVATE KEY / RSA PRIVATE KEY). PKCS8
// (PRIVATE KEY) перекодирует; традиционный — оставляет как есть.
func toTraditionalKeyPEM(keyPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("приватный ключ не PEM")
	}
	switch block.Type {
	case "EC PRIVATE KEY", "RSA PRIVATE KEY":
		return keyPEM, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("разбор PKCS8-ключа: %w", err)
		}
		switch kk := k.(type) {
		case *ecdsa.PrivateKey:
			der, err := x509.MarshalECPrivateKey(kk)
			if err != nil {
				return nil, err
			}
			return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
		case *rsa.PrivateKey:
			return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(kk)}), nil
		default:
			return nil, fmt.Errorf("неподдержанный тип ключа %T для импорта в keychain", k)
		}
	default:
		return nil, fmt.Errorf("неподдержанный PEM-блок ключа: %s", block.Type)
	}
}
