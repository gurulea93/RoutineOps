package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListAuditLog_Empty_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/audit-log", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var entries []any
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entries == nil {
		t.Error("expected non-nil array (even when empty)")
	}
}

func TestListAuditLog_WithEntries_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	// создаём устройство → пишется запись аудита "create_device"
	createDevice(t, rtr, tok, "host-audit", "linux")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/audit-log", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var entries []map[string]any
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) == 0 {
		t.Error("expected at least one audit entry after create_device")
	}
}

func TestListAuditLog_FilterByAction_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	// гарантируем хотя бы одну create_device-запись
	createDevice(t, rtr, tok, "host-audit-filter", "macOS")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/audit-log?action=create_device", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var entries []map[string]any
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) == 0 {
		t.Fatal("expected at least one create_device entry")
	}
	// фильтр должен вернуть только записи с action=create_device
	for _, e := range entries {
		if e["action"] != "create_device" {
			t.Errorf("filtered result contains action=%q, want only create_device", e["action"])
		}
	}
}

func TestListAuditLog_Unauthenticated_Returns401(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/audit-log", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}
