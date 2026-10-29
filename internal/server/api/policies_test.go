package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// createPolicy — хелпер: создаёт политику ПО (allowed/forbidden), возвращает id.
func createPolicy(t *testing.T, rtr http.Handler, tok, software, ruleType string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"software_name": software, "rule_type": ruleType})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/policies", body, tok)
	if w.Code != http.StatusCreated {
		t.Fatalf("createPolicy %s/%s: %d %s", software, ruleType, w.Code, w.Body)
	}
	var p map[string]any
	json.NewDecoder(w.Body).Decode(&p)
	return p["id"].(string)
}

func TestCreatePolicy_AllowedAndForbidden_Returns201(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	for _, ruleType := range []string{"allowed", "forbidden"} {
		id := createPolicy(t, rtr, tok, "soft-"+ruleType, ruleType)
		if id == "" {
			t.Errorf("rule_type %s: empty policy id", ruleType)
		}
	}
}

func TestCreatePolicy_EmptySoftwareName_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	body, _ := json.Marshal(map[string]any{"software_name": "", "rule_type": "forbidden"})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/policies", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body: %s", w.Code, w.Body)
	}
}

func TestCreatePolicy_InvalidRuleType_Returns400(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	body, _ := json.Marshal(map[string]any{"software_name": "tor", "rule_type": "blocked"})
	w := authedDo(t, rtr, http.MethodPost, "/api/v1/policies", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400; body: %s", w.Code, w.Body)
	}
}

func TestDeletePolicy_Returns204(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createPolicy(t, rtr, tok, "soft-to-delete", "forbidden")
	w := authedDo(t, rtr, http.MethodDelete, "/api/v1/policies/"+id, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204; body: %s", w.Code, w.Body)
	}
}

func TestListPolicies_Returns200(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	id := createPolicy(t, rtr, tok, "soft-in-list", "allowed")

	w := authedDo(t, rtr, http.MethodGet, "/api/v1/policies", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", w.Code, w.Body)
	}
	var rules []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&rules); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rules == nil {
		t.Fatal("expected non-nil array")
	}
	var found bool
	for _, p := range rules {
		if p["id"] == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created policy %s not present in list (%d rules)", id, len(rules))
	}
}

// ====== Pass/Fail счётчики ======

// getCompliance дёргает compliance-эндпоинт и возвращает распарсенный массив.
// Тело проверяется сырым: writeJSON от nil-слайса отдал бы "null", и фронт упал бы на
// .map() — ради этого в хендлере стоит nil → [].
func getCompliance(t *testing.T, rtr http.Handler, tok, path string) []map[string]any {
	t.Helper()
	w := authedDo(t, rtr, http.MethodGet, path, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: got %d, want 200; body: %s", path, w.Code, w.Body)
	}
	if body := strings.TrimSpace(w.Body.String()); !strings.HasPrefix(body, "[") {
		t.Fatalf("GET %s: тело %q — ожидали JSON-массив, никогда не null", path, body)
	}
	var rows []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	if rows == nil {
		t.Fatalf("GET %s: декодировался nil-слайс, ожидали непустой указатель", path)
	}
	return rows
}

// Правило видно в compliance, а Checked отражает тип правила: агент проверяет только
// forbidden-список, поэтому у allowed-правил pass/fail врать нельзя.
func TestListPolicyCompliance_Returns200Array(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	forbiddenID := createPolicy(t, rtr, tok, "compliance-forbidden", "forbidden")
	allowedID := createPolicy(t, rtr, tok, "compliance-allowed", "allowed")

	rows := getCompliance(t, rtr, tok, "/api/v1/policies/compliance")
	byRule := map[string]map[string]any{}
	for _, r := range rows {
		byRule[r["rule_id"].(string)] = r
	}

	if got, ok := byRule[forbiddenID]; !ok {
		t.Errorf("forbidden-правило %s отсутствует в compliance", forbiddenID)
	} else if got["checked"] != true {
		t.Errorf("forbidden-правило: checked = %v, want true", got["checked"])
	}

	if got, ok := byRule[allowedID]; !ok {
		t.Errorf("allowed-правило %s отсутствует в compliance", allowedID)
	} else if got["checked"] != false {
		t.Errorf("allowed-правило: checked = %v, want false", got["checked"])
	}
}

func TestListScriptPolicyCompliance_Returns200Array(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	tok := authToken(t, rtr, db)

	policyID := createScriptPolicy(t, rtr, tok, "compliance-policy", "schedule")

	rows := getCompliance(t, rtr, tok, "/api/v1/script-policies/compliance")
	for _, r := range rows {
		if r["policy_id"] != policyID {
			continue
		}
		// Политика никуда не назначена: все счётчики нули, Unknown не уходит в минус.
		for _, field := range []string{"in_scope", "pass", "fail", "unknown"} {
			if v, _ := r[field].(float64); v != 0 {
				t.Errorf("не назначенная политика: %s = %v, want 0", field, v)
			}
		}
		return
	}
	t.Errorf("политика %s отсутствует в compliance", policyID)
}

// Compliance — чтение: viewer его видит, иначе дашборд пуст для всех, кроме админа.
func TestCompliance_ViewerCanRead(t *testing.T) {
	db := newTestDB(t)
	rtr := newRouterFull(t, db)
	viewer := tokenForRole(t, rtr, db, "viewer", "viewer_")

	getCompliance(t, rtr, viewer, "/api/v1/policies/compliance")
	getCompliance(t, rtr, viewer, "/api/v1/script-policies/compliance")
}
