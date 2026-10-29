//go:build screen && !linux

package screen

import (
	"errors"
	"io"
)

// launchPlatform — вне Linux захвата и плашки в этой сборке нет. Windows: кроме того,
// winsession.LaunchInActiveSession не передаёт процессу stdin/stdout (нужно
// наследование хэндлов в CreateProcessAsUser) и не экранирует аргументы — без этого
// канал wire.go не построить.
func launchPlatform(_ string, _ io.Writer) (*workerProc, EndReason, error) {
	return nil, ReasonCaptureUnsupported, errors.New("screen capture is not implemented on this OS")
}
