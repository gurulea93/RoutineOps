//go:build !darwin && !windows

package keystore

import (
	"fmt"
	"runtime"
)

// ProvisionTarget — на платформах без защищённого хранилища пусто (режим file).
func ProvisionTarget() string { return "" }

// ProvisionIsMachineScope — защищённого хранилища нет, машинного scope тоже.
func ProvisionIsMachineScope() bool { return false }

// Import — заглушка: защищённого хранилища нет (используется cert-source=file).
func Import(certPEM, keyPEM []byte, target string) error {
	return fmt.Errorf("импорт в защищённое хранилище не реализован для GOOS=%s", runtime.GOOS)
}
