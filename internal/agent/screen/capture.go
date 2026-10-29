//go:build screen

package screen

import "strings"

// Capturer — источник кадров экрана. Пиксели — упакованный BGRX (байт 3 не используется),
// как отдают и X11 ZPixmap на little-endian, и DXGI: конвертер I420 один на все платформы.
//
// Не потокобезопасен: один захватчик — одна петля кадров.
type Capturer interface {
	// Size — геометрия экрана в пикселях. Постоянна на время жизни захватчика: смена
	// разрешения — новый захватчик и новый кодер.
	Size() (w, h int)
	// Capture снимает кадр. Буфер действителен до следующего Capture.
	Capture() (pix []byte, stride int, err error)
	Close() error
}

// NewScreenCapturer открывает захват основного экрана текущего графического сеанса.
// Реализация — по платформе (capture_linux_x11.go; Windows — Этап B4, macOS — B6). Ошибка
// всегда *ReasonError: менеджер сеанса отдаёт её код оператору.
func NewScreenCapturer() (Capturer, error) { return newPlatformCapturer() }

// x11Availability — можно ли вообще захватывать через X11 при таком окружении. Отдельно от
// подключения, чтобы отказ Wayland был проверяем на любой ОС и не зависел от того, отвечает
// ли XWayland на DISPLAY: под Wayland он отвечает, но показывает только X-окна, и оператор
// увидел бы чёрный экран вместо отказа с кодом.
func x11Availability(getenv func(string) string) (EndReason, bool) {
	if strings.EqualFold(getenv("XDG_SESSION_TYPE"), "wayland") {
		return ReasonWaylandUnsupported, false
	}
	if getenv("DISPLAY") == "" {
		if getenv("WAYLAND_DISPLAY") != "" {
			return ReasonWaylandUnsupported, false
		}
		return ReasonNoConsoleUser, false
	}
	return "", true
}
