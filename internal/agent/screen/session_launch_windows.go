//go:build screen && windows

package screen

import (
	"errors"
	"fmt"
	"io"

	"github.com/Floodww/RoutineOps/internal/agent/winsession"
)

// launchPlatform — Windows: захватчик запускается в активной консольной сессии под токеном
// SYSTEM (winsession.Options.AsSystem, решение владельца от 11.08.2026: иначе UIPI и
// защищённый рабочий стол Winlogon закрыты). Канал — анонимные пайпы с наследованием
// ровно трёх хэндлов. stderr захватчика уходит в NUL: у службы нет консоли, а писать
// лог в stdout нельзя — он занят протоколом, поэтому переданный stderr здесь не используется.
func launchPlatform(exe string, _ io.Writer) (*workerProc, EndReason, error) {
	p, err := winsession.Launch(exe, []string{"screen-worker"}, winsession.Options{AsSystem: true})
	if err != nil {
		if errors.Is(err, winsession.ErrNoActiveSession) {
			return nil, ReasonNoConsoleUser, err
		}
		return nil, "", fmt.Errorf("start screen-worker: %w", err)
	}
	return &workerProc{stdin: p.Stdin, stdout: p.Stdout, wait: p.Wait, kill: p.Kill}, "", nil
}
