//go:build screen && !linux

package screen

import "errors"

// ShowNotice — плашки на этой ОС пока нет (Windows — Этап B4, macOS — B6). Без плашки
// сеанса не бывает: вызывающий отвечает NOTICE_UNAVAILABLE.
func ShowNotice(_, _ string) (Notice, error) {
	return nil, errors.New("screen notice is not implemented on this OS")
}
