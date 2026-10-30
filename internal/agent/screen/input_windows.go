//go:build screen && windows

package screen

import (
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Ввод через SendInput. Процесс захвата работает под SYSTEM в консольной сессии
// сотрудника, поэтому UIPI не мешает попадать и в окна «от администратора». Защищённый
// рабочий стол (UAC, Ctrl+Alt+Del) отсюда недоступен — это режим обслуживания, отдельный шаг.
//
// Структура INPUT описана для 64-разрядной Windows (union выровнен по 8). Агент для
// Windows публикуется только как amd64.

const platformInputAvailable = true

var pSendInput = user32.NewProc("SendInput")

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove        = 0x0001
	mouseLeftDown    = 0x0002
	mouseLeftUp      = 0x0004
	mouseRightDown   = 0x0008
	mouseRightUp     = 0x0010
	mouseMiddleDown  = 0x0020
	mouseMiddleUp    = 0x0040
	mouseWheel       = 0x0800
	mouseHWheel      = 0x1000
	mouseAbsolute    = 0x8000
	mouseVirtualDesk = 0x4000

	keyExtended = 0x0001
	keyKeyUp    = 0x0002
	keyUnicode  = 0x0004
	keyScancode = 0x0008

	wheelDelta = 120
)

type inputRec struct {
	typ uint32
	_   uint32
	u   [4]uint64 // union: MOUSEINPUT (32 байта) покрывает KEYBDINPUT
}

type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keybdInput struct {
	vk, scan uint16
	flags    uint32
	time     uint32
	extra    uintptr
}

func (r *inputRec) mouse() *mouseInput { return (*mouseInput)(unsafe.Pointer(&r.u[0])) }
func (r *inputRec) key() *keybdInput   { return (*keybdInput)(unsafe.Pointer(&r.u[0])) }

type winInjector struct {
	mu      sync.Mutex
	buttons map[int32]bool
	keys    map[string]bool

	// Монитор, не являющийся основным: тогда координаты считаются по виртуальному столу.
	rect    monitorRect
	virtual bool
}

// SetDisplay нацеливает мышь на монитор idx (0 — основной, как раньше).
func (w *winInjector) SetDisplay(idx int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ms := listMonitors()
	if idx <= 0 || idx >= len(ms) {
		w.virtual = false
		return
	}
	w.rect, w.virtual = ms[idx], true
}

// pos — позиция события и доп. флаги SendInput.
func (w *winInjector) pos(ev *pb.InputEvent, fw, fh int) (x, y int32, extra uint32, ok bool) {
	if !w.virtual {
		x, y, ok = absPos(ev, fw, fh)
		return x, y, 0, ok
	}
	vx, vy, vw, vh := virtualDesk()
	x, y, ok = virtualPos(w.rect, vx, vy, vw, vh, int(ev.GetX()), int(ev.GetY()), fw, fh)
	return x, y, mouseVirtualDesk, ok
}

func newPlatformInjector(srcW, srcH int) (Injector, error) {
	return &winInjector{buttons: map[int32]bool{}, keys: map[string]bool{}}, nil
}

func sendInputs(recs ...inputRec) {
	if len(recs) == 0 {
		return
	}
	_, _, _ = pSendInput.Call(uintptr(len(recs)), uintptr(unsafe.Pointer(&recs[0])), unsafe.Sizeof(recs[0]))
}

func (w *winInjector) Close() { w.ReleaseAll() }

func (w *winInjector) Apply(in *pb.SessionInput) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if in.GetReleaseAll() {
		w.releaseLocked()
	}
	fw, fh := int(in.GetFrameWidth()), int(in.GetFrameHeight())
	for _, ev := range in.GetEvents() {
		w.eventLocked(ev, fw, fh)
	}
}

func (w *winInjector) ReleaseAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.releaseLocked()
}

func (w *winInjector) releaseLocked() {
	for b := range w.buttons {
		sendInputs(mouseRec(buttonFlag(b, false), 0, 0, 0))
	}
	w.buttons = map[int32]bool{}
	for k := range w.keys {
		if sc, ext, ok := scanOf(k); ok {
			sendInputs(keyRec(sc, ext, true))
		}
	}
	w.keys = map[string]bool{}
}

func (w *winInjector) eventLocked(ev *pb.InputEvent, fw, fh int) {
	switch ev.GetKind() {
	case "mouse_move":
		if x, y, ex, ok := w.pos(ev, fw, fh); ok {
			sendInputs(mouseRec(mouseMove|mouseAbsolute|ex, x, y, 0))
		}
	case "mouse_down", "mouse_up":
		down := ev.GetKind() == "mouse_down"
		flag := buttonFlag(ev.GetButton(), down)
		if flag == 0 {
			return
		}
		var recs []inputRec
		if x, y, ex, ok := w.pos(ev, fw, fh); ok {
			recs = append(recs, mouseRec(mouseMove|mouseAbsolute|ex, x, y, 0))
		}
		sendInputs(append(recs, mouseRec(flag, 0, 0, 0))...)
		if down {
			w.buttons[ev.GetButton()] = true
		} else {
			delete(w.buttons, ev.GetButton())
		}
	case "wheel":
		var recs []inputRec
		if y := ev.GetWheelY(); y != 0 {
			recs = append(recs, mouseRec(mouseWheel, 0, 0, uint32(int32(y)*wheelDelta)))
		}
		if x := ev.GetWheelX(); x != 0 {
			recs = append(recs, mouseRec(mouseHWheel, 0, 0, uint32(int32(x)*wheelDelta)))
		}
		sendInputs(recs...)
	case "key_down", "key_up":
		sc, ext, ok := scanOf(ev.GetCode())
		if !ok {
			return // неизвестная клавиша — пропуск, а не подмена
		}
		up := ev.GetKind() == "key_up"
		sendInputs(keyRec(sc, ext, up))
		if up {
			delete(w.keys, ev.GetCode())
		} else {
			w.keys[ev.GetCode()] = true
		}
	case "text":
		var recs []inputRec
		for _, u := range utf16.Encode([]rune(ev.GetText())) {
			for _, up := range []bool{false, true} {
				r := inputRec{typ: inputKeyboard}
				k := r.key()
				k.scan, k.flags = u, keyUnicode
				if up {
					k.flags |= keyKeyUp
				}
				recs = append(recs, r)
			}
		}
		sendInputs(recs...)
	}
}

// absPos переводит пиксели кадра в абсолютные координаты SendInput (0..65535 по основному
// экрану). Нормировка только по размерам кадра: масштаб до реального экрана сокращается.
func absPos(ev *pb.InputEvent, fw, fh int) (x, y int32, ok bool) {
	if fw < 2 || fh < 2 {
		return 0, 0, false
	}
	cx, cy := int(ev.GetX()), int(ev.GetY())
	cx, cy = min(max(cx, 0), fw-1), min(max(cy, 0), fh-1)
	return int32(cx * 65535 / (fw - 1)), int32(cy * 65535 / (fh - 1)), true
}

func buttonFlag(button int32, down bool) uint32 {
	switch button {
	case 0:
		if down {
			return mouseLeftDown
		}
		return mouseLeftUp
	case 1:
		if down {
			return mouseMiddleDown
		}
		return mouseMiddleUp
	case 2:
		if down {
			return mouseRightDown
		}
		return mouseRightUp
	}
	return 0
}

func mouseRec(flags uint32, x, y int32, data uint32) inputRec {
	r := inputRec{typ: inputMouse}
	m := r.mouse()
	m.dx, m.dy, m.mouseData, m.flags = x, y, data, flags
	return r
}

func keyRec(scan uint16, ext, up bool) inputRec {
	r := inputRec{typ: inputKeyboard}
	k := r.key()
	k.scan, k.flags = scan, keyScancode
	if ext {
		k.flags |= keyExtended
	}
	if up {
		k.flags |= keyKeyUp
	}
	return r
}

// scanOf — физическая клавиша W3C KeyboardEvent.code → скан-код (набор 1) и признак E0.
// Именно скан-коды, а не VK: «нажать ту же кнопку» не зависит от раскладки сотрудника.
func scanOf(code string) (scan uint16, ext, ok bool) {
	if s, found := scanTable[code]; found {
		return s, false, true
	}
	if s, found := scanExtTable[code]; found {
		return s, true, true
	}
	return 0, false, false
}

var scanTable = func() map[string]uint16 {
	m := map[string]uint16{
		"Escape": 0x01, "Minus": 0x0C, "Equal": 0x0D, "Backspace": 0x0E, "Tab": 0x0F,
		"BracketLeft": 0x1A, "BracketRight": 0x1B, "Enter": 0x1C, "ControlLeft": 0x1D,
		"Semicolon": 0x27, "Quote": 0x28, "Backquote": 0x29, "ShiftLeft": 0x2A, "Backslash": 0x2B,
		"Comma": 0x33, "Period": 0x34, "Slash": 0x35, "ShiftRight": 0x36, "NumpadMultiply": 0x37,
		"AltLeft": 0x38, "Space": 0x39, "CapsLock": 0x3A, "NumLock": 0x45, "ScrollLock": 0x46,
		"Numpad7": 0x47, "Numpad8": 0x48, "Numpad9": 0x49, "NumpadSubtract": 0x4A,
		"Numpad4": 0x4B, "Numpad5": 0x4C, "Numpad6": 0x4D, "NumpadAdd": 0x4E,
		"Numpad1": 0x4F, "Numpad2": 0x50, "Numpad3": 0x51, "Numpad0": 0x52, "NumpadDecimal": 0x53,
		"IntlBackslash": 0x56, "F11": 0x57, "F12": 0x58,
	}
	for i, c := range "1234567890" {
		m["Digit"+string(c)] = uint16(0x02 + i)
	}
	for i, c := range strings.Split("QWERTYUIOP", "") {
		m["Key"+c] = uint16(0x10 + i)
	}
	for i, c := range strings.Split("ASDFGHJKL", "") {
		m["Key"+c] = uint16(0x1E + i)
	}
	for i, c := range strings.Split("ZXCVBNM", "") {
		m["Key"+c] = uint16(0x2C + i)
	}
	for i := 1; i <= 10; i++ {
		m["F"+itoa(i)] = uint16(0x3A + i)
	}
	return m
}()

var scanExtTable = map[string]uint16{
	"NumpadEnter": 0x1C, "ControlRight": 0x1D, "NumpadDivide": 0x35, "AltRight": 0x38,
	"PrintScreen": 0x37, "Home": 0x47, "ArrowUp": 0x48, "PageUp": 0x49, "ArrowLeft": 0x4B,
	"ArrowRight": 0x4D, "End": 0x4F, "ArrowDown": 0x50, "PageDown": 0x51, "Insert": 0x52,
	"Delete": 0x53, "MetaLeft": 0x5B, "MetaRight": 0x5C, "ContextMenu": 0x5D,
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
