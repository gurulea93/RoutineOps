package keystore

import (
	"testing"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
)

func TestNewFileSource(t *testing.T) {
	p, err := New(Options{Source: SourceFile, CertFile: "c", KeyFile: "k", CAFile: "ca"})
	if err != nil {
		t.Fatalf("file source: %v", err)
	}
	if _, ok := p.(transport.FileCertProvider); !ok {
		t.Fatalf("ожидали FileCertProvider, получили %T", p)
	}
	// Пустой source эквивалентен file.
	if _, err := New(Options{Source: ""}); err != nil {
		t.Fatalf("пустой source должен давать file-провайдер: %v", err)
	}
}

func TestNewUnknownSource(t *testing.T) {
	if _, err := New(Options{Source: "vault"}); err == nil {
		t.Fatal("ожидали ошибку на неизвестный cert-source")
	}
}

// TestNewKeystoreWithoutLabel: без метки keystore недопустим на любой платформе —
// либо «не задана метка» (darwin+cgo), либо «не поддержан в этой сборке».
func TestNewKeystoreWithoutLabel(t *testing.T) {
	if _, err := New(Options{Source: SourceKeystore}); err == nil {
		t.Fatal("ожидали ошибку: keystore без метки/без поддержки")
	}
}
