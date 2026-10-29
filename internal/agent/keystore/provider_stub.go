//go:build !windows && (!darwin || !cgo)

package keystore

import (
	"fmt"
	"runtime"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
)

// newKeychainProvider — заглушка для платформ/сборок без поддержки защищённого
// хранилища. Реальный Keychain доступен только в сборке macOS с CGO.
func newKeychainProvider(Options) (transport.CertProvider, error) {
	return nil, fmt.Errorf("cert-source=keystore не поддержан в этой сборке "+
		"(GOOS=%s, нужна сборка macOS с CGO/Security.framework); используйте -cert-source file",
		runtime.GOOS)
}
