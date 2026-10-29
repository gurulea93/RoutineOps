//go:build screen && !linux

package screen

import "errors"

// Windows (DXGI) — Этап B4, macOS (ScreenCaptureKit) — B6.
func newPlatformCapturer() (Capturer, error) {
	return nil, reasonErr(ReasonCaptureUnsupported, errors.New("screen capture not implemented on this OS yet"))
}
