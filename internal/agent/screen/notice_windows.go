//go:build screen && windows

package screen

import (
	"errors"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Плашка наблюдения: безрамочное окно поверх всех, без фокуса, в верхней части основного
// экрана. Закрыть его сотрудник не может: WM_CLOSE (Alt+F4, диспетчер) игнорируется,
// пока плашку не снял сам захватчик через Close. Раз в секунду окно заново поднимается
// поверх остальных. Процесс — под SYSTEM в сеансе пользователя на winsta0\default, так
// что окно видно сотруднику.

var (
	pRegisterClassExW = user32.NewProc("RegisterClassExW")
	pCreateWindowExW  = user32.NewProc("CreateWindowExW")
	pDefWindowProcW   = user32.NewProc("DefWindowProcW")
	pGetMessageW      = user32.NewProc("GetMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessageW = user32.NewProc("DispatchMessageW")
	pPostMessageW     = user32.NewProc("PostMessageW")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")
	pDestroyWindow    = user32.NewProc("DestroyWindow")
	pSetTimer         = user32.NewProc("SetTimer")
	pSetWindowPos     = user32.NewProc("SetWindowPos")
	pBeginPaint       = user32.NewProc("BeginPaint")
	pEndPaint         = user32.NewProc("EndPaint")
	pGetClientRect    = user32.NewProc("GetClientRect")
	pFillRect         = user32.NewProc("FillRect")
	pDrawTextW        = user32.NewProc("DrawTextW")
	pLoadCursorW      = user32.NewProc("LoadCursorW")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	pGetStockObject   = gdi32.NewProc("GetStockObject")
	pSetBkMode        = gdi32.NewProc("SetBkMode")
	pSetTextColor     = gdi32.NewProc("SetTextColor")
	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy    = 0x0002
	wmPaint      = 0x000F
	wmClose      = 0x0010
	wmEraseBkgnd = 0x0014
	wmTimer      = 0x0113

	wsPopup                                     = 0x80000000
	wsVisible                                   = 0x10000000
	wsExTopmost, wsExToolWindow, wsExNoActivate = 0x8, 0x80, 0x08000000

	swpNoSize, swpNoMove, swpNoActivate, swpShowWindow = 0x1, 0x2, 0x10, 0x40

	dtCenter, dtVCenter, dtSingleLine, dtNoPrefix, dtEndEllipsis = 0x1, 0x4, 0x20, 0x800, 0x8000

	colorBg   = 0x002828B4 // BGR: тёмно-красный
	colorText = 0x00FFFFFF
	noticeH   = 34
	noticeID  = 1
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}
type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

// winNotice — единственная плашка процесса (захватчик показывает одну).
type winNotice struct {
	hwnd    uintptr
	text    []uint16
	closing atomic.Bool
	done    chan struct{}
}

var activeNotice *winNotice // доступ из оконной процедуры, которая живёт на одном потоке

var noticeProc = syscall.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	n := activeNotice
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		if n != nil {
			n.paint(hwnd)
			return 0
		}
	case wmTimer:
		_, _, _ = pSetWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpShowWindow)
		return 0
	case wmClose:
		if n != nil && n.closing.Load() {
			_, _, _ = pDestroyWindow.Call(hwnd)
		}
		return 0 // чужой WM_CLOSE игнорируется
	case wmDestroy:
		_, _, _ = pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
})

// ShowNotice поднимает плашку и возвращается, когда окно создано. Ошибка — плашки нет, и
// вызывающий отвечает NOTICE_UNAVAILABLE: сеанс без плашки не начинается.
func ShowNotice(operator, reason string) (Notice, error) {
	text, err := windows.UTF16FromString(noticeText(operator, reason))
	if err != nil {
		return nil, err
	}
	n := &winNotice{text: text, done: make(chan struct{})}
	ready := make(chan error, 1)
	go n.run(ready)
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return n, nil
	case <-time.After(5 * time.Second):
		return nil, errors.New("notice window not created within 5s")
	}
}

func (n *winNotice) run(ready chan<- error) {
	runtime.LockOSThread() // окно и его очередь сообщений живут на одном потоке
	defer close(n.done)
	activeNotice = n

	_, _, _ = pSetProcessDPIAware.Call()
	inst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	className, _ := windows.UTF16PtrFromString("RoutineOpsScreenNotice")
	wc := wndClassExW{Style: 3, WndProc: noticeProc, Instance: inst, Cursor: cursor, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	// Повторная регистрация того же класса вернёт 0 — не ошибка для нас.
	_, _, _ = pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	width := min(int(sw)-40, 1000)
	if width < 300 {
		width = int(sw)
	}
	x := (int(sw) - width) / 2
	hwnd, _, err := pCreateWindowExW.Call(wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(&n.text[0])),
		wsPopup|wsVisible, uintptr(x), 0, uintptr(width), noticeH, 0, 0, inst, 0)
	if hwnd == 0 {
		ready <- errors.New("CreateWindowEx: " + err.Error())
		return
	}
	n.hwnd = hwnd
	_, _, _ = pSetTimer.Call(hwnd, noticeID, 1000, 0)
	_, _, _ = pSetWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpShowWindow)
	ready <- nil

	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 — WM_QUIT, -1 — ошибка
			return
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (n *winNotice) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc rect
	_, _, _ = pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	brush, _, _ := pCreateSolidBrush.Call(colorBg)
	_, _, _ = pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	_, _, _ = pDeleteObject.Call(brush)
	font, _, _ := pGetStockObject.Call(17) // DEFAULT_GUI_FONT
	_, _, _ = pSelectObject.Call(hdc, font)
	_, _, _ = pSetBkMode.Call(hdc, 1) // TRANSPARENT
	_, _, _ = pSetTextColor.Call(hdc, colorText)
	_, _, _ = pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&n.text[0])), ^uintptr(0),
		uintptr(unsafe.Pointer(&rc)), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
	_, _, _ = pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

// Close снимает плашку: единственный путь закрыть её. Ждёт выхода оконного потока.
func (n *winNotice) Close() {
	n.closing.Store(true)
	_, _, _ = pPostMessageW.Call(n.hwnd, wmClose, 0, 0)
	select {
	case <-n.done:
	case <-time.After(2 * time.Second):
	}
}
