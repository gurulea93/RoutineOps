//go:build screen && linux

package screen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/Floodww/RoutineOps/internal/agent/lock"
)

// launchPlatform — Linux: та же логика выбора сессии, что у замка (logind), и запуск
// от имени её пользователя. Под Wayland не запускаем вовсе: XWayland отдал бы только
// X-окна, и оператор увидел бы чёрный экран вместо кода отказа.
func launchPlatform(exe string, stderr io.Writer) (*workerProc, EndReason, error) {
	sess, ok := lock.DiscoverGraphicalSession()
	if !ok {
		return nil, ReasonNoConsoleUser, errors.New("no active graphical session")
	}
	env := lock.ReadSessionEnv(sess)
	switch lock.SessionBackend(sess, env) {
	case lock.BackendWayland:
		return nil, ReasonWaylandUnsupported, fmt.Errorf("session %s is wayland", sess.ID)
	case lock.BackendX11:
	default:
		return nil, ReasonNoConsoleUser, fmt.Errorf("session %s has no graphics", sess.ID)
	}

	cmd := exec.Command(exe, "screen-worker")
	cmd.Env = append(lock.OverlayEnv(sess, env), "XDG_SESSION_TYPE=x11")
	cmd.Stderr = stderr
	attr := &syscall.SysProcAttr{Setsid: true}
	if os.Geteuid() == 0 && os.Geteuid() != sess.UID {
		u, err := user.LookupId(strconv.Itoa(sess.UID))
		if err != nil {
			return nil, "", fmt.Errorf("lookup session user %d: %w", sess.UID, err)
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			return nil, "", fmt.Errorf("session user gid %q: %w", u.Gid, err)
		}
		attr.Credential = &syscall.Credential{Uid: uint32(sess.UID), Gid: uint32(gid)}
	}
	cmd.SysProcAttr = attr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start screen-worker: %w", err)
	}
	return &workerProc{
		stdin:  stdin,
		stdout: stdout,
		wait:   cmd.Wait,
		kill:   func() { _ = cmd.Process.Kill() },
	}, "", nil
}
