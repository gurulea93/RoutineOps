//go:build windows

package winsession

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process — процесс, запущенный в сессии пользователя, с каналами stdin/stdout.
type Process struct {
	Stdin  io.WriteCloser // служба → процесс
	Stdout io.ReadCloser  // процесс → служба
	Wait   func() error   // дождаться выхода; ненулевой код выхода — ошибка
	Kill   func()         // TerminateProcess
}

// Launch — то же, что LaunchInActiveSessionOpts, но процессу передаются анонимные пайпы
// как stdin/stdout (канал служба ↔ захватчик, internal/agent/screen/wire.go).
//
// Наследуются РОВНО три хэндла: конец stdin, конец stdout и NUL под stderr
// (PROC_THREAD_ATTRIBUTE_HANDLE_LIST). Без списка bInheritHandles=TRUE отдал бы ребёнку
// все наследуемые хэндлы службы, а служба работает под LocalSystem. Родительские концы
// пайпов не наследуемые. stderr уходит в NUL: у службы нет консоли, а писать лог в
// stdout нельзя — он занят протоколом.
//
// Аргументы экранируются по правилам разбора командной строки Windows
// (windows.ComposeCommandLine), в отличие от buildCmdLine трея и оверлея.
func Launch(exe string, args []string, opt Options) (*Process, error) {
	sid := windows.WTSGetActiveConsoleSessionId()
	if sid == 0xFFFFFFFF {
		return nil, ErrNoActiveSession
	}
	primary, err := sessionPrimaryToken(sid, opt.AsSystem)
	if err != nil {
		return nil, err
	}
	defer primary.Close()

	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, primary, false); err != nil {
		env = nil
	}
	defer func() {
		if env != nil {
			windows.DestroyEnvironmentBlock(env)
		}
	}()

	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, sa, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe(stdin): %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, sa, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("CreatePipe(stdout): %w", err)
	}
	// Родительские концы не должны уехать ребёнку.
	_ = windows.SetHandleInformation(inW, windows.HANDLE_FLAG_INHERIT, 0)
	_ = windows.SetHandleInformation(outR, windows.HANDLE_FLAG_INHERIT, 0)

	nul, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return nil, err
	}
	errH, err := windows.CreateFile(nul, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		sa, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("open NUL: %w", err)
	}
	childEnds := []windows.Handle{inR, outW, errH}
	closeAll := func() {
		for _, h := range []windows.Handle{inR, inW, outR, outW, errH} {
			windows.CloseHandle(h)
		}
	}

	al, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}
	defer al.Delete()
	if err := al.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&childEnds[0]), uintptr(len(childEnds))*unsafe.Sizeof(childEnds[0])); err != nil {
		closeAll()
		return nil, fmt.Errorf("handle list: %w", err)
	}

	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		closeAll()
		return nil, err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Desktop = desktop
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = inR, outW, errH
	si.ProcThreadAttributeList = al.List()
	var pi windows.ProcessInformation

	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcessAsUser(primary, nil, cmdLine, nil, nil, true, flags, env, nil,
		(*windows.StartupInfo)(unsafe.Pointer(&si)), &pi)
	// Детские концы родителю больше не нужны ни при успехе, ни при ошибке.
	for _, h := range childEnds {
		windows.CloseHandle(h)
	}
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	proc := pi.Process

	return &Process{
		Stdin:  os.NewFile(uintptr(inW), "screen-worker-stdin"),
		Stdout: os.NewFile(uintptr(outR), "screen-worker-stdout"),
		Wait: func() error {
			defer windows.CloseHandle(proc)
			if ev, err := windows.WaitForSingleObject(proc, windows.INFINITE); err != nil || ev != windows.WAIT_OBJECT_0 {
				return fmt.Errorf("WaitForSingleObject: event %d err %v", ev, err)
			}
			var code uint32
			if err := windows.GetExitCodeProcess(proc, &code); err != nil {
				return err
			}
			if code != 0 {
				return errors.New("exit code " + fmt.Sprint(code))
			}
			return nil
		},
		Kill: func() { _ = windows.TerminateProcess(proc, 1) },
	}, nil
}
