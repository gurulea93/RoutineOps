package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"
)

// httpCheck успешно разбирает корректный manifest и проставляет query os/arch/current.
func TestHTTPCheckSuccess(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(Manifest{
			Version: "v2.0.0", URL: "http://x/bin", SHA256: "abc", Signature: "sig",
		})
	}))
	defer srv.Close()

	m, err := httpCheck(context.Background(), srv.Client(), srv.URL, "v1.0.0")
	if err != nil {
		t.Fatalf("httpCheck: %v", err)
	}
	if m.Version != "v2.0.0" || m.URL != "http://x/bin" {
		t.Fatalf("разобрали не тот manifest: %+v", m)
	}
	if gotQuery.Get("os") != runtime.GOOS || gotQuery.Get("arch") != runtime.GOARCH || gotQuery.Get("current") != "v1.0.0" {
		t.Fatalf("query параметры не проставлены: %v", gotQuery)
	}
}

func TestHTTPCheckErrors(t *testing.T) {
	// Не-200 статус.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "нет такой версии", http.StatusNotFound)
	}))
	defer bad.Close()
	if _, err := httpCheck(context.Background(), bad.Client(), bad.URL, "v1"); err == nil {
		t.Error("ожидали ошибку на HTTP 404")
	}

	// Битый JSON.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{не json"))
	}))
	defer junk.Close()
	if _, err := httpCheck(context.Background(), junk.Client(), junk.URL, "v1"); err == nil {
		t.Error("ожидали ошибку на битом JSON")
	}

	// Неполный manifest (нет подписи/URL).
	incomplete := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Manifest{Version: "v2.0.0"})
	}))
	defer incomplete.Close()
	if _, err := httpCheck(context.Background(), incomplete.Client(), incomplete.URL, "v1"); err == nil {
		t.Error("ожидали ошибку на неполном manifest")
	}

	// Некорректный URL.
	if _, err := httpCheck(context.Background(), http.DefaultClient, "://нет-схемы", "v1"); err == nil {
		t.Error("ожидали ошибку на некорректном URL")
	}
}

// httpDownload скачивает тело при 200 и возвращает ошибку при не-200.
func TestHTTPDownload(t *testing.T) {
	want := []byte("бинарь-агента")
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(want)
	}))
	defer ok.Close()

	data, err := httpDownload(context.Background(), ok.Client(), ok.URL)
	if err != nil {
		t.Fatalf("httpDownload: %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("скачали не то: %q", data)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "пропал", http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := httpDownload(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Error("ожидали ошибку на HTTP 500")
	}
}
