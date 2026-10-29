package winsession

import "testing"

// TestBuildCmdLine: путь к бинарю всегда в кавычках (терпит пробелы в Program
// Files), аргументы добавляются через пробел в исходном порядке.
func TestBuildCmdLine(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		args []string
		want string
	}{
		{"трей", `C:\Program Files\RoutineOps\RoutineOps-agent.exe`, []string{"tray"},
			`"C:\Program Files\RoutineOps\RoutineOps-agent.exe" tray`},
		{"лок-экран", `C:\Program Files\RoutineOps\RoutineOps-agent.exe`, []string{"lock-screen"},
			`"C:\Program Files\RoutineOps\RoutineOps-agent.exe" lock-screen`},
		{"без аргументов", `C:\mdm.exe`, nil, `"C:\mdm.exe"`},
		{"несколько аргументов", `C:\mdm.exe`, []string{"a", "b"}, `"C:\mdm.exe" a b`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildCmdLine(c.exe, c.args); got != c.want {
				t.Fatalf("buildCmdLine(%q, %v) = %q, хотим %q", c.exe, c.args, got, c.want)
			}
		})
	}
}
