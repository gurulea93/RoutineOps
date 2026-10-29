//go:build screen && windows

package screen

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Захват основного экрана через GDI (BitBlt в DIB-секцию). Без CGO и без зависимостей,
// работает на обычном рабочем столе сеанса. НЕ видит защищённый рабочий стол (UAC,
// Ctrl+Alt+Del, экран блокировки): BitBlt там не удаётся, и сеанс закроется
// CAPTURE_FAILED. Захват через DXGI и переключение на рабочий стол Winlogon — отдельный шаг.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pGetDC              = user32.NewProc("GetDC")
	pReleaseDC          = user32.NewProc("ReleaseDC")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	pSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
)

const (
	smCxScreen = 0
	smCyScreen = 1
	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000 // видеть и слоистые окна
)

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type gdiCapturer struct {
	screenDC, memDC, bmp, old uintptr
	pix                       []byte
	w, h                      int
}

func newPlatformCapturer() (Capturer, error) {
	// Без DPI-осведомлённости Windows отдаёт виртуализированные размеры, а не физические.
	_, _, _ = pSetProcessDPIAware.Call()
	w, _, _ := pGetSystemMetrics.Call(smCxScreen)
	h, _, _ := pGetSystemMetrics.Call(smCyScreen)
	if w == 0 || h == 0 {
		return nil, reasonErr(ReasonNoConsoleUser, fmt.Errorf("screen metrics %dx%d", w, h))
	}
	c := &gdiCapturer{w: int(w), h: int(h)}

	c.screenDC, _, _ = pGetDC.Call(0)
	if c.screenDC == 0 {
		return nil, reasonErr(ReasonCaptureFailed, fmt.Errorf("GetDC failed"))
	}
	c.memDC, _, _ = pCreateCompatibleDC.Call(c.screenDC)
	if c.memDC == 0 {
		c.Close()
		return nil, reasonErr(ReasonCaptureFailed, fmt.Errorf("CreateCompatibleDC failed"))
	}
	// Отрицательная высота — строки сверху вниз, как ждёт TileEncoder. 32 бита = BGRX.
	bi := bitmapInfoHeader{Width: int32(c.w), Height: -int32(c.h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	c.bmp, _, _ = pCreateDIBSection.Call(c.memDC, uintptr(unsafe.Pointer(&bi)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if c.bmp == 0 || bits == nil {
		c.Close()
		return nil, reasonErr(ReasonCaptureFailed, fmt.Errorf("CreateDIBSection failed"))
	}
	c.pix = unsafe.Slice((*byte)(bits), 4*c.w*c.h)
	c.old, _, _ = pSelectObject.Call(c.memDC, c.bmp)
	return c, nil
}

func (c *gdiCapturer) Size() (int, int) { return c.w, c.h }

func (c *gdiCapturer) Capture() ([]byte, int, error) {
	ok, _, _ := pBitBlt.Call(c.memDC, 0, 0, uintptr(c.w), uintptr(c.h), c.screenDC, 0, 0, srcCopy|captureBlt)
	if ok == 0 {
		return nil, 0, reasonErr(ReasonCaptureFailed, fmt.Errorf("BitBlt failed (secure desktop?)"))
	}
	return c.pix, 4 * c.w, nil
}

func (c *gdiCapturer) Close() error {
	if c.memDC != 0 && c.old != 0 {
		_, _, _ = pSelectObject.Call(c.memDC, c.old)
	}
	if c.bmp != 0 {
		_, _, _ = pDeleteObject.Call(c.bmp)
	}
	if c.memDC != 0 {
		_, _, _ = pDeleteDC.Call(c.memDC)
	}
	if c.screenDC != 0 {
		_, _, _ = pReleaseDC.Call(0, c.screenDC)
	}
	c.pix = nil
	return nil
}
