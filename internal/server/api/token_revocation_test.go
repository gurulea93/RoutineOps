package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLogout_RevokesToken_Returns401 проверяет M-7: после logout тот же токен
// больше не пускает на защищённый роут (jti попал в блок-лист).
func TestLogout_RevokesToken_Returns401(t *testing.T) {
	rtr, db := newRouterWithDB(t)
	seedUser(t, db, "revoke@test.com", "secret123", "it_admin")

	// Логинимся и достаём значение httpOnly cookie "token".
	lw := doLogin(t, rtr, "revoke@test.com", "secret123")
	if lw.Code != http.StatusOK {
		t.Fatalf("login: got %d, want 200; body: %s", lw.Code, lw.Body)
	}
	var token string
	for _, c := range lw.Result().Cookies() {
		if c.Name == "token" {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no token cookie in login response")
	}

	cookie := &http.Cookie{Name: "token", Value: token}

	// Sanity: токен работает на защищённом роуте.
	r1 := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r1.AddCookie(cookie)
	w1 := httptest.NewRecorder()
	rtr.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("protected GET before logout: got %d, want 200; body: %s", w1.Code, w1.Body)
	}

	// Logout той же кукой → 204.
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	r2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	rtr.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204; body: %s", w2.Code, w2.Body)
	}

	// Тот же токен теперь отозван → 401.
	r3 := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	r3.AddCookie(cookie)
	w3 := httptest.NewRecorder()
	rtr.ServeHTTP(w3, r3)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("protected GET after logout: got %d, want 401; body: %s", w3.Code, w3.Body)
	}
}
