// Package keystore выбирает источник mTLS-материала агента: файлы на диске
// (по умолчанию) или защищённое хранилище ОС (macOS Keychain / Windows
// Certificate Store). Приватный ключ в режиме keystore не покидает хранилище —
// подпись TLS-хендшейка делает ОС (crypto.Signer поверх SecKey/NCrypt).
//
// Граница абстракции — transport.CertProvider: остальной
// код агента не знает, откуда взялись cert/key/CA.
//
// Реальная реализация Keychain доступна только в сборке darwin с CGO
// (Security.framework). CGO-free сборка (как кросс-сборка в CI, CGO_ENABLED=0)
// компилирует заглушку — режим keystore в ней недоступен, используется file.
package keystore

import (
	"fmt"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
)

// Источники сертификатов.
const (
	SourceFile     = "file"     // cert/key/CA — файлы на диске (FileCertProvider)
	SourceKeystore = "keystore" // защищённое хранилище ОС (Keychain/Cert Store)
)

// Options — параметры выбора провайдера.
type Options struct {
	Source string // file | keystore

	// Пути для файлового провайдера и для CA (CA публичен и в режиме keystore
	// читается из файла — секрета в нём нет).
	CertFile string
	KeyFile  string
	CAFile   string

	// Label — метка идентичности в хранилище ОС (обычно device_id = CN серта,
	// который проставил сервер при энроллменте, ADR-1).
	Label string
}

// New возвращает провайдер mTLS-материала по выбранному источнику.
func New(o Options) (transport.CertProvider, error) {
	switch o.Source {
	case "", SourceFile:
		return transport.FileCertProvider{
			CertFile: o.CertFile,
			KeyFile:  o.KeyFile,
			CAFile:   o.CAFile,
		}, nil
	case SourceKeystore:
		return newKeychainProvider(o)
	default:
		return nil, fmt.Errorf("неизвестный cert-source %q (ожидается file|keystore)", o.Source)
	}
}
