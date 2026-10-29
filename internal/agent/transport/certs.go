package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// CertProvider отдаёт mTLS-материал для соединения с сервером.
//
// Граница абстракции (намеренно узкая): сейчас это
// файлы на диске (FileCertProvider). После MVP сюда подставится реализация
// поверх Keychain (macOS) / Certificate Store (Windows) — без изменений в
// остальном коде агента.
type CertProvider interface {
	// ClientCertificate — сертификат+ключ агента (CN = device_id, ADR-1).
	ClientCertificate() (tls.Certificate, error)
	// RootCAs — пул доверенных CA для проверки серверного сертификата.
	RootCAs() (*x509.CertPool, error)
}

// FileCertProvider читает cert/key/CA из файлов на диске.
type FileCertProvider struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

func (p FileCertProvider) ClientCertificate() (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load client cert/key (%s, %s): %w", p.CertFile, p.KeyFile, err)
	}
	return cert, nil
}

func (p FileCertProvider) RootCAs() (*x509.CertPool, error) {
	pem, err := os.ReadFile(p.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file %s: %w", p.CAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates in CA file %s", p.CAFile)
	}
	return pool, nil
}
