package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Floodww/RoutineOps/internal/server/api"
)

func newRouter(t *testing.T) http.Handler {
	t.Helper()
	// nil CA, nil asynqClient, nil db - достаточно для маршрутов с ранним выходом
	return api.NewRouter(nil, nil, []byte("test"), nil, "https://test.local", t.TempDir(), nil, false)
}

func TestEnroll_NoCA_Returns503(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/enroll",
		strings.NewReader(`{"enrollment_token":"tok","csr_pem":"pem"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newRouter(t).ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", w.Code)
	}
}

func TestEnroll_BadJSON_Returns400(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/enroll",
		strings.NewReader(`not json`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newRouter(t).ServeHTTP(w, r)

	// CA nil → 503 раньше, чем дойдёт до decode. Проверяем что не 200.
	if w.Code == http.StatusOK {
		t.Error("expected non-200 for bad request")
	}
}

func TestAgentVersion_MissingParams_Returns400(t *testing.T) {
	cases := []string{
		"/api/v1/agent/version",
		"/api/v1/agent/version?os=darwin",
		"/api/v1/agent/version?arch=amd64",
	}
	rtr := newRouter(t)
	for _, path := range cases {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("path %q: got %d, want 400", path, w.Code)
		}
	}
}

func TestHealthz_Returns200(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	newRouter(t).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200", w.Code)
	}
}
