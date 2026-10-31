//go:build screen && windows

package screen

import (
	"errors"
	"time"
	"unicode/utf16"
	"unsafe"
)

// Буфер обмена сотрудника (текст). Процесс захвата сидит на рабочем столе сотрудника
// (WinSta0\Default), поэтому OpenClipboard работает с ЕГО буфером. Используется только в
// сеансах с управлением: читать чужой буфер без управления — это слежка, а не помощь.

var (
	pOpenClipboard              = user32.NewProc("OpenClipboard")
	pCloseClipboard             = user32.NewProc("CloseClipboard")
	pEmptyClipboard             = user32.NewProc("EmptyClipboard")
	pSetClipboardData           = user32.NewProc("SetClipboardData")
	pGetClipboardData           = user32.NewProc("GetClipboardData")
	pGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	pIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	pGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	pGlobalFree                 = kernel32.NewProc("GlobalFree")
	pGlobalLock                 = kernel32.NewProc("GlobalLock")
	pGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	pLstrlenW                   = kernel32.NewProc("lstrlenW")
	pRtlMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// openClipboard — буфер бывает занят другим приложением на миллисекунды: несколько попыток.
func openClipboard() error {
	for i := 0; i < 10; i++ {
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("clipboard is busy")
}

func clipboardSeq() uint32 {
	r, _, _ := pGetClipboardSequenceNumber.Call()
	return uint32(r)
}

func setClipboardText(s string) error {
	u := utf16.Encode([]rune(s))
	u = append(u, 0)
	size := uintptr(len(u) * 2)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		_, _, _ = pGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	_, _, _ = pRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	_, _, _ = pGlobalUnlock.Call(h)

	if err := openClipboard(); err != nil {
		_, _, _ = pGlobalFree.Call(h)
		return err
	}
	defer pCloseClipboard.Call()
	_, _, _ = pEmptyClipboard.Call()
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		_, _, _ = pGlobalFree.Call(h) // владельцем память становится система только при успехе
		return errors.New("SetClipboardData failed")
	}
	return nil
}

// getClipboardText — текст буфера; "" — текста нет. maxRunes — потолок (длиннее не читаем).
func getClipboardText(maxRunes int) (string, error) {
	if r, _, _ := pIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	if err := openClipboard(); err != nil {
		return "", err
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("GlobalLock failed")
	}
	defer pGlobalUnlock.Call(h)
	n, _, _ := pLstrlenW.Call(p)
	if n == 0 {
		return "", nil
	}
	if int(n) > maxRunes*2 {
		n = uintptr(maxRunes * 2)
	}
	buf := make([]uint16, n)
	_, _, _ = pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, n*2)
	return string(utf16.Decode(buf)), nil
}
