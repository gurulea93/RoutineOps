//go:build screen

package screen

import (
	"bytes"
	"io"
	"sync"
)

// ctlWriter — единственный писатель stdin захватчика. Пишут несколько горутин (запрос
// ключевого кадра из latestFrame, команды сервера из loopDown), поэтому сообщение
// собирается целиком в формате wire.go и уходит одним Write под мьютексом: два частичных
// Write разных горутин перемешали бы заголовок одного сообщения с телом другого.
type ctlWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// send пишет сообщение [type][len][payload] одним Write.
func (c *ctlWriter) send(typ byte, payload []byte) error {
	var buf bytes.Buffer
	if err := writeMsg(&buf, typ, payload); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.w.Write(buf.Bytes())
	return err
}

// askKeyframe — запрос ключевого кадра (msgKeyframe) для latestFrame. Ошибка не
// поднимается: канал умер — это заметит readPipe по EOF и закроет сеанс WORKER_LOST.
func (c *ctlWriter) askKeyframe() { _ = c.send(msgKeyframe, nil) }
