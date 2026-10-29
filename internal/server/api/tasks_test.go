package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestListTasks_Empty_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-tasks-empty", "macos")

	w := authedDo(t, rtr, http.MethodGet, fmt.Sprintf("/api/v1/devices/%s/tasks", deviceID), nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var tasks []any
	json.NewDecoder(w.Body).Decode(&tasks)
	if tasks == nil {
		t.Error("expected non-nil array")
	}
}

func TestCreateTask_MissingScriptContent_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-task-bad1", "macos")
	body := []byte(`{"platform":"macos"}`)
	w := authedDo(t, rtr, http.MethodPost, fmt.Sprintf("/api/v1/devices/%s/tasks", deviceID), body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

func TestCreateTask_MissingPlatform_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-task-bad2", "windows")
	body := []byte(`{"script_content":"echo hello"}`)
	w := authedDo(t, rtr, http.MethodPost, fmt.Sprintf("/api/v1/devices/%s/tasks", deviceID), body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

func TestCreateTask_BadJSON_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	deviceID, _ := createDevice(t, rtr, tok, "host-task-badjson", "macos")
	w := authedDo(t, rtr, http.MethodPost, fmt.Sprintf("/api/v1/devices/%s/tasks", deviceID), []byte(`not json`), tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

func TestCreateTask_Unauthenticated_Returns401(t *testing.T) {
	w := authedDo(t, newRouter(t), http.MethodPost, "/api/v1/devices/some-id/tasks",
		[]byte(`{"script_content":"x","platform":"macos"}`), "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}

func TestLockDevice_Returns200WithPassword(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)
	deviceID, _ := createDevice(t, rtr, tok, "host-lock", "windows")
	body := []byte(`{"reason":"тест"}`)
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/devices/"+deviceID+"/lock", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["task_id"] == "" {
		t.Error("expected non-empty task_id")
	}
	if len(resp["password"]) != 12 {
		t.Errorf("password len = %d, want 12", len(resp["password"]))
	}
}

func TestLockDevice_EmptyReason_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)
	deviceID, _ := createDevice(t, rtr, tok, "host-lock-empty", "windows")
	body := []byte(`{}`)
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/devices/"+deviceID+"/lock", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
}

func TestLockDevice_Unauthorized_Returns401(t *testing.T) {
	w := authedDo(t, newRouter(t), http.MethodPost, "/api/v1/devices/some-id/lock", []byte(`{"reason":"x"}`), "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}

func TestUnlockDevice_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)
	deviceID, _ := createDevice(t, rtr, tok, "host-unlock", "windows")
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/devices/"+deviceID+"/unlock", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["task_id"] == "" {
		t.Error("expected non-empty task_id")
	}
}
