//go:build screen

package screen

import "testing"

func TestHubSetDisplay(t *testing.T) {
	h := newHub(100, 100)
	h.tenantID, h.operatorID = "t1", "u1"
	if got := h.setDisplay("t1", "u1", 1); got != displayBadIndex {
		t.Fatalf("before first frame any index is invalid, got %v", got)
	}
	h.displays = 2
	if got := h.setDisplay("t1", "other", 1); got != displayNotYours {
		t.Fatalf("foreign operator: %v", got)
	}
	if got := h.setDisplay("t1", "u1", 2); got != displayBadIndex {
		t.Fatalf("out of range: %v", got)
	}
	if got := h.setDisplay("t1", "u1", 1); got != displayOK {
		t.Fatalf("ok: %v", got)
	}
	h.control = true
	d, allow := h.narrowScope()
	if d != 1 || !allow {
		t.Fatalf("scope %d %v: switching must keep control", d, allow)
	}
	h.returned = true
	if _, allow = h.narrowScope(); allow {
		t.Fatal("returned control must not be allowed")
	}
}
