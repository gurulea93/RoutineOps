package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// createScriptPolicy — хелпер: создаёт скрипт-политику с указанным триггером,
// возвращает её id. Для schedule/event подкладывает валидный config.
func createScriptPolicy(t *testing.T, rtr http.Handler, tok, name, trigger string) string {
	t.Helper()
	scriptID := createScript(t, rtr, tok, name+"-script", "linux")

	payload := map[string]any{"name": name, "script_id": scriptID, "trigger_type": trigger}
	switch trigger {
	case "schedule":
		payload["schedule_config"] = json.RawMessage(`{"cron":"* * * * *"}`)
	case "event_trigger":
		payload["event_trigger_config"] = json.RawMessage(`{"event":"login"}`)
	}
	body, _ := json.Marshal(payload)
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/script-policies", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("createScriptPolicy %s/%s: %d %s", name, trigger, w.Code, w.Body)
	}
	var p map[string]any
	json.NewDecoder(w.Body).Decode(&p)
	return p["id"].(string)
}

func TestCreateScriptPolicy_AllTriggers_Returns201(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	for _, trigger := range []string{"on_connect", "schedule", "event_trigger"} {
		id := createScriptPolicy(t, rtr, tok, "pol-"+trigger, trigger)
		if id == "" {
			t.Errorf("trigger %s: empty policy id", trigger)
		}
	}
}

func TestCreateScriptPolicy_InvalidTrigger_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	scriptID := createScript(t, rtr, tok, "pol-bad-trigger", "linux")
	body, _ := json.Marshal(map[string]string{
		"name": "bad", "script_id": scriptID, "trigger_type": "on_boot",
	})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/script-policies", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body: %s", w.Code, w.Body)
	}
}

func TestCreateScriptPolicy_EmptyFields_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	cases := map[string]map[string]string{
		"no name":      {"name": "", "script_id": "x", "trigger_type": "on_connect"},
		"no script_id": {"name": "x", "script_id": "", "trigger_type": "on_connect"},
		"no trigger":   {"name": "x", "script_id": "x", "trigger_type": ""},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(payload)
			w := authedDo(t, rtr, http.MethodPost, "/api/v1/script-policies", body, tok)
			if w.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400; body: %s", w.Code, w.Body)
			}
		})
	}
}

func TestDeleteScriptPolicy_Returns204(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createScriptPolicy(t, rtr, tok, "pol-to-delete", "on_connect")
	w := authedDo(t, rtr, http.MethodDelete, "/api/v1/script-policies/"+id, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestToggleScriptPolicy_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createScriptPolicy(t, rtr, tok, "pol-to-toggle", "on_connect")

	body, _ := json.Marshal(map[string]bool{"active": false})
	w := authedDo(t, rtr, http.MethodPatch, "/api/v1/script-policies/"+id+"/toggle", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp map[string]bool
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["active"] != false {
		t.Errorf("active = %v, want false", resp["active"])
	}
}

// ====== Device Groups ======

// createGroup — хелпер: создаёт группу устройств, возвращает её id.
func createGroup(t *testing.T, rtr http.Handler, tok, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/device-groups", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("createGroup %s: %d %s", name, w.Code, w.Body)
	}
	var g map[string]any
	json.NewDecoder(w.Body).Decode(&g)
	return g["id"].(string)
}

func TestCreateDeviceGroup_Returns201(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createGroup(t, rtr, tok, "group-create")
	if id == "" {
		t.Error("empty group id")
	}
}

func TestCreateDeviceGroup_EmptyName_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	body, _ := json.Marshal(map[string]string{"name": ""})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/device-groups", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body: %s", w.Code, w.Body)
	}
}

func TestDeleteDeviceGroup_Returns204(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createGroup(t, rtr, tok, "group-to-delete")
	w := authedDo(t, rtr, http.MethodDelete, "/api/v1/device-groups/"+id, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestGroupMembers_AddRemove(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	groupID := createGroup(t, rtr, tok, "group-members")
	deviceID, _ := createDevice(t, rtr, tok, "host-group-member", "linux")

	// add member → 204
	body, _ := json.Marshal(map[string]string{"device_id": deviceID})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/device-groups/"+groupID+"/members", body, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("add member: got %d, want 204; body: %s", w.Code, w.Body)
	}

	// add with empty device_id → 400
	bad, _ := json.Marshal(map[string]string{"device_id": ""})
	w = authedDo(t, rtr, http.MethodPost, "/api/v1/device-groups/"+groupID+"/members", bad, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("add empty member: got %d, want 400", w.Code)
	}

	// remove member → 204
	w = authedDo(t, rtr, http.MethodDelete, "/api/v1/device-groups/"+groupID+"/members/"+deviceID, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("remove member: got %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestGroupPolicies_AssignUnassign(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	groupID := createGroup(t, rtr, tok, "group-policies")
	policyID := createScriptPolicy(t, rtr, tok, "pol-for-group", "on_connect")

	// assign → 204
	body, _ := json.Marshal(map[string]string{"policy_id": policyID})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/device-groups/"+groupID+"/policies", body, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("assign policy: got %d, want 204; body: %s", w.Code, w.Body)
	}

	// unassign → 204
	w = authedDo(t, rtr, http.MethodDelete, "/api/v1/device-groups/"+groupID+"/policies/"+policyID, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("unassign policy: got %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestListScriptPolicies_Empty_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	// Clear existing policies via API to ensure empty list
	wList := authedDo(t, rtr, http.MethodGet, "/api/v1/script-policies", nil, tok)
	var existing []map[string]any
	json.NewDecoder(wList.Body).Decode(&existing)
	for _, p := range existing {
		authedDo(t, rtr, http.MethodDelete, "/api/v1/script-policies/"+p["id"].(string), nil, tok)
	}

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/script-policies", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var policies []map[string]any
	json.NewDecoder(w.Body).Decode(&policies)
	if policies == nil || len(policies) != 0 {
		t.Errorf("expected empty list [], got %v", policies)
	}
}

func TestListScriptPolicies_WithPolicies_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	createScriptPolicy(t, rtr, tok, "pol-list-test", "on_connect")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/script-policies", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var policies []map[string]any
	json.NewDecoder(w.Body).Decode(&policies)
	if len(policies) == 0 {
		t.Errorf("expected policies to be returned")
	}
}

func TestListDeviceGroups_Empty_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	// Clear existing groups via API to ensure empty list
	wList := authedDo(t, rtr, http.MethodGet, "/api/v1/device-groups", nil, tok)
	var existing []map[string]any
	json.NewDecoder(wList.Body).Decode(&existing)
	for _, g := range existing {
		authedDo(t, rtr, http.MethodDelete, "/api/v1/device-groups/"+g["id"].(string), nil, tok)
	}

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/device-groups", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var groups []map[string]any
	json.NewDecoder(w.Body).Decode(&groups)
	if groups == nil || len(groups) != 0 {
		t.Errorf("expected empty list [], got %v", groups)
	}
}

func TestListDeviceGroups_WithGroup_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	createGroup(t, rtr, tok, "list-group-test")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/device-groups", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var groups []map[string]any
	json.NewDecoder(w.Body).Decode(&groups)
	if len(groups) == 0 {
		t.Errorf("expected groups to be returned")
	}
}

// Cron валидируется тем же парсером, которым его исполняет агент. Иначе политика с
// пустым или битым выражением создавалась с 201 и молча никогда не запускалась.
func TestCreateScriptPolicy_CronValidation(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)
	scriptID := createScript(t, rtr, tok, "cron-script", "linux")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"валидный cron", map[string]any{
			"name": "ok", "script_id": scriptID, "trigger_type": "schedule",
			"schedule_config": map[string]string{"cron": "0 9 * * 1-5"},
		}, http.StatusCreated},
		{"дескриптор @daily", map[string]any{
			"name": "daily", "script_id": scriptID, "trigger_type": "schedule",
			"schedule_config": map[string]string{"cron": "@daily"},
		}, http.StatusCreated},
		{"cron отсутствует", map[string]any{
			"name": "nocron", "script_id": scriptID, "trigger_type": "schedule",
		}, http.StatusBadRequest},
		{"пустой cron", map[string]any{
			"name": "emptycron", "script_id": scriptID, "trigger_type": "schedule",
			"schedule_config": map[string]string{"cron": "  "},
		}, http.StatusBadRequest},
		{"битый cron", map[string]any{
			"name": "badcron", "script_id": scriptID, "trigger_type": "schedule",
			"schedule_config": map[string]string{"cron": "99 * * *"},
		}, http.StatusBadRequest},
		{"on_connect не требует cron", map[string]any{
			"name": "onconnect", "script_id": scriptID, "trigger_type": "on_connect",
		}, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			w := authedDo(t, rtr, http.MethodPost, "/api/v1/script-policies", body, tok)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d; body: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

// Скрипт, на который ссылается политика, удалять нельзя — 409, а не 500.
func TestDeleteScript_InUse_409(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	scriptID := createScript(t, rtr, tok, "used-script", "linux")
	body, _ := json.Marshal(map[string]any{
		"name": "uses-script", "script_id": scriptID, "trigger_type": "on_connect",
	})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/script-policies", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("create policy: got %d; body: %s", w.Code, w.Body)
	}

	w = authedDo(t, rtr, http.MethodDelete, "/api/v1/scripts/"+scriptID, nil, tok)
	if w.Code != http.StatusConflict {
		t.Errorf("delete script in use: got %d, want 409; body: %s", w.Code, w.Body)
	}
}
