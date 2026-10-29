//go:build screen

package screen

import (
	"strings"
	"testing"
)

func TestNoticeTextStripsControlsAndClips(t *testing.T) {
	got := noticeText("op\r\nFAKE: ok", strings.Repeat("ж", 300))
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("control characters survived: %q", got)
	}
	if rs := []rune(got); len(rs) > 60+120+80 {
		t.Fatalf("too long: %d", len(rs))
	}
	if !strings.Contains(got, "op  FAKE: ok") {
		t.Fatalf("operator mangled: %q", got)
	}
}
