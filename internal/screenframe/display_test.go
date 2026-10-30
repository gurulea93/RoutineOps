//go:build screen

package screenframe

import "testing"

func TestSetDisplayInfoRoundTrip(t *testing.T) {
	parts, err := Split(7, true, 100, 50, nil, MaxPartBytes)
	if err != nil || len(parts) == 0 {
		t.Fatalf("split: %v", err)
	}
	SetDisplayInfo(parts[0], 3, 2)
	p, err := ParsePart(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.Displays != 3 || p.Display != 2 || !p.Keyframe || !p.Last {
		t.Fatalf("got %+v", p)
	}
	SetDisplayInfo(parts[0], 99, -1) // зажимается в 0..15
	p, _ = ParsePart(parts[0])
	if p.Displays != 15 || p.Display != 0 {
		t.Fatalf("clamp: %+v", p)
	}
}
