package security

import "testing"

func TestFindForbidden(t *testing.T) {
	procs := []Process{
		{PID: 1, Name: "bash", Cmd: "/bin/bash"},
		{PID: 2, Name: "sleep", Cmd: "/bin/sleep"},
		{PID: 3, Name: "Foo", Cmd: "/Applications/Foo.app/Contents/MacOS/Foo"},
	}
	forbidden := []string{"sleep", "foo"} // уже в нижнем регистре (как из loadForbidden)

	got := findForbidden(procs, forbidden)

	if p, ok := got["sleep"]; !ok || p.PID != 2 {
		t.Errorf("'sleep' должен матчить pid 2, got=%+v ok=%v", p, ok)
	}
	if p, ok := got["foo"]; !ok || p.PID != 3 {
		t.Errorf("'foo' должен матчить pid 3 (регистронезависимо), got=%+v ok=%v", p, ok)
	}
	if len(got) != 2 {
		t.Errorf("лишние совпадения: %v", got)
	}
}

func TestFindForbiddenNone(t *testing.T) {
	procs := []Process{{PID: 1, Name: "bash", Cmd: "/bin/bash"}}
	if got := findForbidden(procs, []string{"sleep"}); len(got) != 0 {
		t.Errorf("ожидали 0 совпадений, got=%v", got)
	}
}
