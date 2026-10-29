package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Run по тику проверяет и применяет доступное обновление.
func TestRunTickerAppliesUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	bin := []byte("новый-бинарь")
	m := signedManifest("v2.0.0", bin, priv)

	applied := make(chan []byte, 4)
	u := &Updater{
		current:  "v1.0.0",
		pubKey:   pub,
		interval: 5 * time.Millisecond,
		log:      discardLog(),
		check:    func(context.Context) (*Manifest, error) { return m, nil },
		download: func(context.Context, string) ([]byte, error) { return bin, nil },
		replace: func(data []byte) error {
			select {
			case applied <- data:
			default:
			}
			return nil
		},
		restart: func() {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)

	select {
	case got := <-applied:
		if string(got) != string(bin) {
			t.Fatalf("применён не тот бинарь: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Run не применил обновление за отведённое время")
	}
}

// Run продолжает тикать после ошибки проверки (не выходит из цикла).
func TestRunContinuesAfterCheckError(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	var checks int32
	u := &Updater{
		current:  "v1.0.0",
		pubKey:   pub,
		interval: 5 * time.Millisecond,
		log:      discardLog(),
		check: func(context.Context) (*Manifest, error) {
			atomic.AddInt32(&checks, 1)
			return nil, errors.New("сервер недоступен")
		},
		download: func(context.Context, string) ([]byte, error) { return nil, nil },
		replace:  func([]byte) error { return nil },
		restart:  func() {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&checks) < 2 {
		select {
		case <-deadline:
			t.Fatalf("Run перестал тикать после ошибки (checks=%d)", atomic.LoadInt32(&checks))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// New с непрогружаемым CA всё равно собирает рабочий Updater (системные корни +
// предупреждение), сеймы установлены.
func TestNewWithUnloadableCA(t *testing.T) {
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	u := New("v1.0.0", time.Hour, pub, "http://example/check",
		filepath.Join(t.TempDir(), "несуществующий-ca.pem"), "", func() {}, discardLog())
	if u.check == nil || u.download == nil || u.replace == nil {
		t.Fatal("сеймы Updater не установлены")
	}
	if u.current != "v1.0.0" {
		t.Fatalf("current=%q", u.current)
	}
}
