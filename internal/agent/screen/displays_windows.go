//go:build screen && windows

package screen

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	pGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")

	monMu  sync.Mutex
	monAcc []monitorRect
	monCB  = windows.NewCallback(enumMonitorProc) // число callback'ов в процессе ограничено: один на всё время
)

type rect32 struct{ Left, Top, Right, Bottom int32 }

// monitorInfo — MONITORINFO.
type monitorInfo struct {
	Size    uint32
	Monitor rect32
	Work    rect32
	Flags   uint32
}

const monitorPrimary = 0x1

func enumMonitorProc(hMon, _, _, _ uintptr) uintptr {
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := pGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
		monAcc = append(monAcc, monitorRect{
			X: int(mi.Monitor.Left), Y: int(mi.Monitor.Top),
			W: int(mi.Monitor.Right - mi.Monitor.Left), H: int(mi.Monitor.Bottom - mi.Monitor.Top),
			Primary: mi.Flags&monitorPrimary != 0,
		})
	}
	return 1
}

// listMonitors — мониторы текущего рабочего стола в порядке orderMonitors. Пустой список —
// перечисление не удалось; вызывающий откатывается на основной экран.
func listMonitors() []monitorRect {
	monMu.Lock()
	defer monMu.Unlock()
	monAcc = nil
	_, _, _ = pSetProcessDPIAware.Call()
	_, _, _ = pEnumDisplayMonitors.Call(0, 0, monCB, 0)
	return orderMonitors(monAcc)
}

func platformDisplayCount() int { return max(1, len(listMonitors())) }

// virtualDesk — границы виртуального рабочего стола (все мониторы вместе).
func virtualDesk() (x, y, w, h int) {
	const smX, smY, smCX, smCY = 76, 77, 78, 79
	gm := func(i uintptr) int { v, _, _ := pGetSystemMetrics.Call(i); return int(int32(v)) }
	return gm(smX), gm(smY), gm(smCX), gm(smCY)
}

func newPlatformCapturerAt(idx int) (Capturer, error) {
	if idx == 0 {
		return newPlatformCapturer()
	}
	ms := listMonitors()
	if idx < 0 || idx >= len(ms) {
		return nil, reasonErr(ReasonCaptureFailed, errNoSuchDisplay)
	}
	return newGDICapturer(ms[idx])
}
