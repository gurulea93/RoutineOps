//go:build screen && !windows

package screen

import "errors"

// Буфер обмена пока только на Windows (как и ввод).

func clipboardSeq() uint32 { return 0 }

func setClipboardText(string) error {
	return errors.New("clipboard is not implemented on this platform")
}

func getClipboardText(int) (string, error) { return "", nil }
