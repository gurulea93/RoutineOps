package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/credentials/insecure"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// badDialer — Dialer с заведомо некорректным адресом: grpc.NewClient на нём
// падает, что даёт детерминированную ошибку Dial без реального сервера.
func badDialer() *Dialer {
	return &Dialer{serverAddr: "\n", creds: insecure.NewCredentials()}
}

// New задаёт разумные дефолты backoff/blockedRetry.
func TestNewDefaults(t *testing.T) {
	c := New(nil, discardLog())
	if c.baseBackoff != time.Second {
		t.Errorf("baseBackoff=%v, ожидали 1s", c.baseBackoff)
	}
	if c.maxBackoff != 60*time.Second {
		t.Errorf("maxBackoff=%v, ожидали 60s", c.maxBackoff)
	}
	if c.minHealthy != 5*time.Second {
		t.Errorf("minHealthy=%v, ожидали 5s", c.minHealthy)
	}
	if c.blockedRetry != 5*time.Minute {
		t.Errorf("blockedRetry=%v, ожидали 5m", c.blockedRetry)
	}
}

// SetBlockedRetry применяет только положительное значение, иначе оставляет дефолт.
func TestSetBlockedRetry(t *testing.T) {
	c := New(nil, discardLog())
	c.SetBlockedRetry(30 * time.Second)
	if c.blockedRetry != 30*time.Second {
		t.Fatalf("ожидали 30s, got %v", c.blockedRetry)
	}
	c.SetBlockedRetry(0) // невалидное — игнор
	if c.blockedRetry != 30*time.Second {
		t.Fatalf("0 не должен менять значение, got %v", c.blockedRetry)
	}
	c.SetBlockedRetry(-time.Second) // отрицательное — игнор
	if c.blockedRetry != 30*time.Second {
		t.Fatalf("отрицательное не должно менять значение, got %v", c.blockedRetry)
	}
}

// Run немедленно возвращает ошибку контекста, если ctx уже отменён, не пытаясь
// устанавливать соединение.
func TestRunReturnsOnCancelledContext(t *testing.T) {
	c := New(nil, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sessionCalled := false
	err := c.Run(ctx, func(context.Context, Stream) error {
		sessionCalled = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидали context.Canceled, got %v", err)
	}
	if sessionCalled {
		t.Fatal("session не должна вызываться при отменённом контексте")
	}
}

// connectAndServe возвращает ошибку, если соединение установить не удалось,
// и не вызывает session.
func TestConnectAndServeDialError(t *testing.T) {
	c := New(badDialer(), discardLog())
	called := false
	err := c.connectAndServe(context.Background(), func(context.Context, Stream) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("ожидали ошибку Dial на некорректном адресе")
	}
	if called {
		t.Fatal("session не должна вызываться при ошибке соединения")
	}
}

// Run переживает ошибку соединения и продолжает попытки с backoff, пока ctx жив;
// по истечении ctx возвращает его ошибку.
func TestRunRetriesThenStopsOnTimeout(t *testing.T) {
	c := New(badDialer(), discardLog())
	c.baseBackoff = 5 * time.Millisecond
	c.maxBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	var sessionCalled bool
	err := c.Run(ctx, func(context.Context, Stream) error {
		sessionCalled = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидали DeadlineExceeded, got %v", err)
	}
	if sessionCalled {
		t.Fatal("session не должна вызываться: соединение всегда падает")
	}
}
