//go:build screen && linux

package screen

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/Floodww/RoutineOps/internal/agent/x11ui"
)

// Плашка «экран просматривается» (§4): полоса поверх всех окон у верхнего края экрана.
//
// Свойства, ради которых она устроена так:
//   - override-redirect — окно вне управления WM: его нельзя свернуть, закрыть или
//     переставить, и флага отключения у плашки нет ни у кого;
//   - фокус и ввод НЕ забираются — сотрудник продолжает работать, плашка только сообщает;
//   - раз в секунду окно поднимается наверх: новое окно (уведомление, полноэкранный
//     плеер) могло всплыть поверх и спрятать её.
//
// Плашка рисуется в том же X-сеансе, который захватывается, поэтому видна и оператору:
// он видит ровно то, что знает сотрудник.

const noticeHeight = 28

var noticeTitle = struct{ ru, en string }{"Экран просматривается", "Your screen is being viewed"}

type x11Notice struct {
	conn *xgb.Conn
	win  xproto.Window
	gcBg xproto.Gcontext
	gcFg xproto.Gcontext
	font xproto.Font
	two  bool
	w    uint16
	text string

	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// ShowNotice поднимает плашку в X-сеансе из DISPLAY. Ошибка — плашки нет, и сеанса быть
// не должно (вызывающий отвечает NOTICE_UNAVAILABLE).
func ShowNotice(operator, reason string) (Notice, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11 connect: %w", err)
	}
	n := &x11Notice{conn: conn, stop: make(chan struct{})}
	if err := n.setup(operator, reason); err != nil {
		conn.Close()
		return nil, err
	}
	n.wg.Add(2)
	go n.events()
	go n.keepOnTop()
	return n, nil
}

func (n *x11Notice) setup(operator, reason string) error {
	scr := xproto.Setup(n.conn).DefaultScreen(n.conn)
	n.w = scr.WidthInPixels
	win, err := xproto.NewWindowId(n.conn)
	if err != nil {
		return err
	}
	n.win = win
	const bg = 0xb00020
	err = xproto.CreateWindowChecked(n.conn, scr.RootDepth, win, scr.Root,
		0, 0, n.w, noticeHeight, 0, xproto.WindowClassInputOutput, scr.RootVisual,
		xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{bg, 1, uint32(xproto.EventMaskExposure | xproto.EventMaskVisibilityChange)}).Check()
	if err != nil {
		return fmt.Errorf("x11 create window: %w", err)
	}
	const name = "RoutineOps screen notice"
	xproto.ChangeProperty(n.conn, xproto.PropModeReplace, win, xproto.AtomWmName,
		xproto.AtomString, 8, uint32(len(name)), []byte(name))
	class := []byte("routineops-screen-notice\x00RoutineOps\x00")
	xproto.ChangeProperty(n.conn, xproto.PropModeReplace, win, xproto.AtomWmClass,
		xproto.AtomString, 8, uint32(len(class)), class)

	f := x11ui.PickFonts(n.conn, slog.Default())
	n.font, n.two = f.Text, f.TwoByte
	title := noticeTitle.en
	if n.two {
		title = noticeTitle.ru
	}
	n.text = title + ": " + operator
	if reason != "" {
		n.text += " — " + reason
	}
	if !n.two {
		n.text = asciiOnly(n.text)
	}
	if n.gcBg, err = n.newGC(bg, 0); err != nil {
		return err
	}
	if n.gcFg, err = n.newGC(0xffffff, n.font); err != nil {
		return err
	}
	if err := xproto.MapWindowChecked(n.conn, win).Check(); err != nil {
		return fmt.Errorf("x11 map: %w", err)
	}
	n.raise()
	n.draw()
	// Плашка должна реально стоять на экране к моменту hello: синхронный запрос
	// гарантирует, что сервер X обработал всё выше.
	attr, err := xproto.GetWindowAttributes(n.conn, win).Reply()
	if err != nil {
		return fmt.Errorf("x11 attributes: %w", err)
	}
	if attr.MapState != xproto.MapStateViewable {
		return fmt.Errorf("x11: notice window is not viewable (state %d)", attr.MapState)
	}
	return nil
}

func (n *x11Notice) newGC(fg uint32, font xproto.Font) (xproto.Gcontext, error) {
	gc, err := xproto.NewGcontextId(n.conn)
	if err != nil {
		return 0, err
	}
	mask := uint32(xproto.GcForeground | xproto.GcBackground)
	vals := []uint32{fg, 0xb00020}
	if font != 0 {
		mask |= xproto.GcFont
		vals = append(vals, uint32(font))
	}
	return gc, xproto.CreateGCChecked(n.conn, gc, xproto.Drawable(n.win), mask, vals).Check()
}

func (n *x11Notice) raise() {
	xproto.ConfigureWindow(n.conn, n.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
}

func (n *x11Notice) draw() {
	xproto.PolyFillRectangle(n.conn, xproto.Drawable(n.win), n.gcBg,
		[]xproto.Rectangle{{X: 0, Y: 0, Width: n.w, Height: noticeHeight}})
	x11ui.DrawCentered(n.conn, xproto.Drawable(n.win), n.gcFg, n.font, n.two, n.text, noticeHeight-9, n.w)
}

func (n *x11Notice) events() {
	defer n.wg.Done()
	for {
		ev, err := n.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // соединение закрыто
		}
		switch ev.(type) {
		case xproto.ExposeEvent:
			n.draw()
		case xproto.VisibilityNotifyEvent:
			n.raise()
			n.draw()
		}
	}
}

func (n *x11Notice) keepOnTop() {
	defer n.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-n.stop:
			return
		case <-t.C:
			n.raise()
			n.draw()
		}
	}
}

func (n *x11Notice) Close() {
	n.once.Do(func() {
		close(n.stop)
		xproto.DestroyWindow(n.conn, n.win)
		n.conn.Sync()
		n.conn.Close()
		n.wg.Wait()
	})
}

// asciiOnly — латиница для X-сервера без Unicode-шрифтов: лучше «?» вместо буквы, чем
// пустые квадраты, по которым не понять, кто смотрит.
func asciiOnly(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r == '—' {
			r = '-'
		}
		if r < 0x20 || r > 0x7e {
			r = '?'
		}
		b = append(b, byte(r))
	}
	return string(b)
}
