package silentuninstall

import (
	"reflect"
	"testing"
)

func fake(content string) ReadHead {
	return func(string, int) []byte { return []byte(content) }
}

func TestDeriveInnoByName(t *testing.T) {
	got, ok := Derive(`"C:\Program Files\AIMP\unins000.exe" /x`, nil)
	want := []string{`C:\Program Files\AIMP\unins000.exe`, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestDeriveInnoBySignature(t *testing.T) {
	_, ok := Derive(`C:\App\remove.exe`, fake("xx Inno Setup Setup Data xx"))
	if !ok {
		t.Fatal("Inno signature not recognised")
	}
}

func TestDeriveNSIS(t *testing.T) {
	got, ok := Derive(`"C:\Program Files (x86)\Brave\uninst.exe"`, fake("\xef\xbe\xad\xdeNullsoftInst"))
	want := []string{`C:\Program Files (x86)\Brave\uninst.exe`, "/S"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestDeriveUnquotedPathWithSpaces(t *testing.T) {
	got, ok := Derive(`C:\Program Files\X\unins001.exe /SILENT`, nil)
	if !ok || got[0] != `C:\Program Files\X\unins001.exe` {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestDeriveUnknownIsNotSilent(t *testing.T) {
	cases := []string{
		``,
		`"C:\App\setup.exe" /uninstall`,
		`MsiExec.exe /I{ABC}`,
		`"unterminated`,
	}
	for _, c := range cases {
		if _, ok := Derive(c, fake("nothing special")); ok {
			t.Errorf("%q must not be silent", c)
		}
	}
}
