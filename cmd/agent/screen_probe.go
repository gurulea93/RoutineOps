//go:build screen

package main

import (
	"fmt"
	"io"
)

// runScreenProbe — диагностика захвата появится вместе с менеджером сеанса.
func runScreenProbe(_, stderr io.Writer, _ []string) int {
	fmt.Fprintln(stderr, "screen-probe: not implemented yet")
	return 1
}

// runScreenWorker — процесс захвата поднимает только менеджер сеанса, которого пока нет.
func runScreenWorker(stderr io.Writer, _ []string) int {
	fmt.Fprintln(stderr, "screen-worker: not implemented yet")
	return 1
}
