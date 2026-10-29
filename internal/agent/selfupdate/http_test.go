package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// TestHTTPCheckSendsOSArchCurrent: httpCheck должен добавлять os/arch/current —
// сервер по ним выбирает бинарь и без них отвечает 400 (поймано на e2e).
func TestHTTPCheckSendsOSArchCurrent(t *testing.T) {
	var gotOS, gotArch, gotCurrent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOS = r.URL.Query().Get("os")
		gotArch = r.URL.Query().Get("arch")
		gotCurrent = r.URL.Query().Get("current")
		json.NewEncoder(w).Encode(Manifest{Version: "v1.2.3", URL: "u", SHA256: "s", Signature: "g"})
	}))
	defer srv.Close()

	m, err := httpCheck(context.Background(), srv.Client(), srv.URL, "v1.0.0")
	if err != nil {
		t.Fatalf("httpCheck: %v", err)
	}
	if m.Version != "v1.2.3" {
		t.Fatalf("version=%q", m.Version)
	}
	if gotOS != runtime.GOOS || gotArch != runtime.GOARCH {
		t.Errorf("os/arch = %q/%q, want %q/%q", gotOS, gotArch, runtime.GOOS, runtime.GOARCH)
	}
	if gotCurrent != "v1.0.0" {
		t.Errorf("current = %q, want v1.0.0", gotCurrent)
	}
}
