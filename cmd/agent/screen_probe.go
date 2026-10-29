//go:build screen

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/Floodww/RoutineOps/internal/agent/screen"
)

// runScreenProbe — диагностика захвата появится вместе с менеджером сеанса.
func runScreenProbe(_, stderr io.Writer, _ []string) int {
	fmt.Fprintln(stderr, "screen-probe: not implemented yet")
	return 1
}

// runScreenWorker — процесс захвата. Своими руками его не запускают: его поднимает служба
// агента в графической сессии сотрудника и говорит с ним по stdin/stdout. Всё, что не
// протокол, идёт в stderr: stdout занят каналом кадров.
func runScreenWorker(stderr io.Writer, _ []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	code := screen.RunWorkerProcess(ctx, os.Stdin, os.Stdout)
	if code != 0 {
		fmt.Fprintln(stderr, "screen-worker: exited with protocol error")
	}
	return code
}
