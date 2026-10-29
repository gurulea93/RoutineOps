//go:build screen && !(vpx && cgo)

package screen

import (
	"errors"
	"testing"
)

// Без libvpx агент обязан остаться на RSF1: ни кодера, ни способности.
func TestNoVPXFallsBackToRSF1(t *testing.T) {
	if _, err := NewVideoEncoder("vp8", EncoderConfig{Width: 64, Height: 64}); !errors.Is(err, ErrCodecUnavailable) {
		t.Fatalf("err = %v, want ErrCodecUnavailable", err)
	}
	if caps := VideoCapabilities(); len(caps) != 0 {
		t.Fatalf("capabilities without vpx: %v", caps)
	}
}
