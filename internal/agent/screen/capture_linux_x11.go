//go:build screen && linux

package screen

import (
	"fmt"
	"os"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// Захват X11 через GetImage(ZPixmap) корневого окна. Чистый Go (xgb уже в go.mod ради
// lockui), поэтому линуксовый агент не тянет CGO ради захвата. MIT-SHM быстрее на больших
// экранах — оптимизация на потом; профиль low всё равно ограничен 15 кадрами.
//
// Агент-служба работает от root вне графического сеанса: захватчик запускается в сеансе
// пользователя (как lockui), где DISPLAY и XAUTHORITY уже правильные.
type x11Capturer struct {
	conn *xgb.Conn
	root xproto.Window
	w, h int
}

func newPlatformCapturer() (Capturer, error) {
	if r, ok := x11Availability(os.Getenv); !ok {
		return nil, reasonErr(r, nil)
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, reasonErr(ReasonNoConsoleUser, err)
	}
	c, err := newX11Capturer(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func newX11Capturer(conn *xgb.Conn) (*x11Capturer, error) {
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	// Поддерживаем то, что встречается на практике: 24/32-битный цвет, 32 бита на пиксель,
	// little-endian (байты B,G,R,X). Остальное — отказ кодом, а не перекошенные цвета.
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return nil, reasonErr(ReasonCaptureUnsupported, fmt.Errorf("x11 image byte order MSB first"))
	}
	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == screen.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if (screen.RootDepth != 24 && screen.RootDepth != 32) || bpp != 32 {
		return nil, reasonErr(ReasonCaptureUnsupported, fmt.Errorf("x11 depth %d, %d bpp", screen.RootDepth, bpp))
	}
	return &x11Capturer{conn: conn, root: screen.Root, w: int(screen.WidthInPixels), h: int(screen.HeightInPixels)}, nil
}

func (c *x11Capturer) Size() (int, int) { return c.w, c.h }

func (c *x11Capturer) Capture() ([]byte, int, error) {
	img, err := xproto.GetImage(c.conn, xproto.ImageFormatZPixmap, xproto.Drawable(c.root),
		0, 0, uint16(c.w), uint16(c.h), 0xffffffff).Reply()
	if err != nil {
		return nil, 0, reasonErr(ReasonCaptureFailed, err)
	}
	// 32 бита на пиксель — строка уже выровнена на 32 бита, stride = 4*w.
	if len(img.Data) < 4*c.w*c.h {
		return nil, 0, reasonErr(ReasonCaptureFailed, fmt.Errorf("x11 image %d bytes, want %d", len(img.Data), 4*c.w*c.h))
	}
	return img.Data, 4 * c.w, nil
}

func (c *x11Capturer) Close() error {
	c.conn.Close()
	return nil
}
