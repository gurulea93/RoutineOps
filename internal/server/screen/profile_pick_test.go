//go:build screen

package screen

import "testing"

func TestPickProfile(t *testing.T) {
	cases := []struct{ req, cap, want string }{
		{"", "high", "high"},
		{"low", "high", "low"},
		{"medium", "high", "medium"},
		{"high", "medium", "medium"}, // выше потолка нельзя
		{"high", "low", "low"},
		{"bogus", "medium", "medium"},
		{"medium", "bogus", "low"},
		{"", "", "low"},
		{"ultra", "ultra", "ultra"},
		{"ultra", "high", "high"},
	}
	for _, c := range cases {
		if got := pickProfile(c.req, c.cap); got != c.want {
			t.Errorf("pickProfile(%q,%q)=%q, want %q", c.req, c.cap, got, c.want)
		}
	}
}
