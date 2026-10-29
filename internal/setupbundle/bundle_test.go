package setupbundle

import (
	"bytes"
	"strings"
	"testing"
)

var good = Params{
	EnrollURL: "https://192.168.4.237/api/v1/enroll", EnrollToken: "tok-1",
	CAURL: "https://192.168.4.237/ca.crt", CASHA256: "ab12", ServerAddr: "192.168.4.237:50051",
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("MZ-stub-bytes") // тело stub
	msi := bytes.Repeat([]byte{0xD0, 0xCF}, 1000)
	if err := Append(&buf, msi, good); err != nil {
		t.Fatal(err)
	}
	p, got, err := Read(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || p != good || !bytes.Equal(got, msi) {
		t.Fatalf("params %+v err %v msi equal=%v", p, err, bytes.Equal(got, msi))
	}
}

func TestReadRejectsPlainExe(t *testing.T) {
	plain := bytes.Repeat([]byte("MZ"), 100)
	if _, _, err := Read(bytes.NewReader(plain), int64(len(plain))); err == nil {
		t.Fatal("file without payload must be rejected")
	}
	if _, _, err := Read(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("empty file must be rejected")
	}
}

func TestReadRejectsLyingTrailer(t *testing.T) {
	var buf bytes.Buffer
	_ = Append(&buf, []byte("msi"), good)
	b := buf.Bytes()
	b[len(b)-16] = 0xFF // длина msi больше файла
	b[len(b)-15] = 0xFF
	b[len(b)-14] = 0xFF
	if _, _, err := Read(bytes.NewReader(b), int64(len(b))); err == nil {
		t.Fatal("trailer longer than the file must be rejected")
	}
}

func TestValidate(t *testing.T) {
	bad := good
	bad.EnrollToken = ""
	if bad.Validate() == nil {
		t.Fatal("empty token must be rejected")
	}
	bad = good
	bad.ServerAddr = `x" & calc & "`
	if bad.Validate() == nil {
		t.Fatal("quote in a value must be rejected: it would escape the msiexec command line")
	}
}

func TestMSIArgs(t *testing.T) {
	a := good.MSIArgs(`C:\t\a.msi`, `C:\t\a.log`)
	for _, want := range []string{`/i "C:\t\a.msi"`, `/l*v "C:\t\a.log"`, `ENROLL_TOKEN="tok-1"`,
		`CA_SHA256="ab12"`, `SERVER_ADDR="192.168.4.237:50051"`} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
}
