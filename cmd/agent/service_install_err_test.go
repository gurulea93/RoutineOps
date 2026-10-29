package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Floodww/RoutineOps/internal/agent/service"
)

// TestServiceInstallFatal: сбой трея (macOS) или несостоявшийся немедленный старт
// (Windows) НЕ должны ронять enroll -install-service — служба уже зарегистрирована,
// а Harden/tamper.Arm ещё впереди. Настоящая ошибка регистрации (SCM/launchctl) —
// фатальна. errors.Is обязан находить сентинел сквозь %w-обёртки любой глубины.
func TestServiceInstallFatal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil — нечего валить", nil, false},
		{"произвольная ошибка регистрации", errors.New("SCM отказал"), true},
		{"старт не удался (обёрнут)", fmt.Errorf("%w (RoutineOps-agent): x", service.ErrServiceStartFailed), false},
		{"трей не встал (обёрнут)", fmt.Errorf("%w: запись agent plist: operation not permitted", service.ErrTrayInstallFailed), false},
		{"трей не встал (двойная обёртка)", fmt.Errorf("обёртка: %w", fmt.Errorf("%w: y", service.ErrTrayInstallFailed)), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceInstallFatal(c.err); got != c.want {
				t.Errorf("serviceInstallFatal(%v) = %v, хотим %v", c.err, got, c.want)
			}
		})
	}
}
