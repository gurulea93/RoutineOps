package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Floodww/RoutineOps/internal/server/storage"
)

// seedAlert создаёт alert напрямую через storage (минуя gRPC gateway).
func seedAlert(t *testing.T, db *storage.DB, deviceID, alertType string) {
	t.Helper()
	_, err := db.CreateAlert(context.Background(), deviceID, alertType, `{"process":"test.exe"}`, "")
	if err != nil {
		t.Fatalf("seedAlert: %v", err)
	}
}

func TestListAlerts_Empty_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/alerts", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var alerts []any
	json.NewDecoder(w.Body).Decode(&alerts)
	if alerts == nil {
		t.Error("expected non-nil array")
	}
}

func TestListAlerts_WithAlerts_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-alerts", "macos")
	seedAlert(t, db, deviceID, "FORBIDDEN_SOFTWARE")
	seedAlert(t, db, deviceID, "UNAUTHORIZED_INSTALL")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/alerts", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var alerts []map[string]any
	json.NewDecoder(w.Body).Decode(&alerts)
	if len(alerts) < 2 {
		t.Errorf("got %d alerts, want at least 2", len(alerts))
	}
}

func TestListAlerts_FilterByDevice_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	d1, _ := createDevice(t, rtr, tok, "host-alert-filter-1", "macos")
	d2, _ := createDevice(t, rtr, tok, "host-alert-filter-2", "windows")
	seedAlert(t, db, d1, "FORBIDDEN_SOFTWARE")
	seedAlert(t, db, d2, "UNAUTHORIZED_INSTALL")

	w := authedDo(t, rtr, http.MethodGet, fmt.Sprintf("/api/v1/alerts?device_id=%s", d1), nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var alerts []map[string]any
	json.NewDecoder(w.Body).Decode(&alerts)
	for _, a := range alerts {
		if a["device_id"].(string) != d1 {
			t.Errorf("alert device_id = %q, want %q", a["device_id"], d1)
		}
	}
}

func TestAcknowledgeAlert_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-ack-alert", "macos")
	seedAlert(t, db, deviceID, "FORBIDDEN_SOFTWARE")

	// получаем список чтобы найти id алерта
	w := authedDo(t, rtr, http.MethodGet, "/api/v1/alerts", nil, tok)
	var alerts []map[string]any
	json.NewDecoder(w.Body).Decode(&alerts)
	if len(alerts) == 0 {
		t.Fatal("no alerts to acknowledge")
	}
	// найти первый необработанный
	var alertID string
	for _, a := range alerts {
		if a["device_id"].(string) == deviceID {
			alertID = a["id"].(string)
			break
		}
	}
	if alertID == "" {
		t.Fatal("alert for device not found in list")
	}

	w2 := authedDo(t, rtr, http.MethodPost, fmt.Sprintf("/api/v1/alerts/%s/acknowledge", alertID), nil, tok)
	if w2.Code != http.StatusOK {
		t.Fatalf("acknowledge: got %d, want 200; body: %s", w2.Code, w2.Body)
	}
	var resp map[string]string
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["status"] != "acknowledged" {
		t.Errorf("status = %q, want acknowledged", resp["status"])
	}
}

func TestAcknowledgeAlert_Unauthenticated_Returns401(t *testing.T) {
	w := authedDo(t, newRouter(t), http.MethodPost, "/api/v1/alerts/some-id/acknowledge", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}
