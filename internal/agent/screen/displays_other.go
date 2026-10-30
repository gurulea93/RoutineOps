//go:build screen && !windows

package screen

// Выбор монитора пока только на Windows; на остальных ОС экран один.
func platformDisplayCount() int { return 1 }

func newPlatformCapturerAt(idx int) (Capturer, error) {
	if idx != 0 {
		return nil, reasonErr(ReasonCaptureUnsupported, errNoMultiDisplay)
	}
	return newPlatformCapturer()
}
