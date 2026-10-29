//go:build screen && !windows

package screen

import "errors"

// Внедрение ввода пока есть только на Windows (SendInput). Linux (XTest) и macOS (CGEvent)
// добавляются отдельно; до тех пор приглашение с управлением получает INPUT_UNAVAILABLE.
const platformInputAvailable = false

func newPlatformInjector(srcW, srcH int) (Injector, error) {
	return nil, errors.New("input injection is not implemented on this platform")
}
