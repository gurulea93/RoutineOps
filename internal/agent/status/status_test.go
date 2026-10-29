package status

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "status.json")
	in := State{
		Version:       "1.2.3",
		DeviceID:      "dev-abc",
		ServerAddr:    "203.0.113.5:50051",
		LastHeartbeat: time.Now().Truncate(time.Second),
	}
	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Version != in.Version || out.DeviceID != in.DeviceID || out.ServerAddr != in.ServerAddr {
		t.Fatalf("round-trip разошёлся: %+v != %+v", out, in)
	}
	if !out.LastHeartbeat.Equal(in.LastHeartbeat) {
		t.Fatalf("LastHeartbeat разошёлся: %v != %v", out.LastHeartbeat, in.LastHeartbeat)
	}
}

func TestOnline(t *testing.T) {
	fresh := State{LastHeartbeat: time.Now()}
	if !fresh.Online(time.Minute) {
		t.Error("свежий heartbeat должен считаться online")
	}
	stale := State{LastHeartbeat: time.Now().Add(-2 * time.Minute)}
	if stale.Online(time.Minute) {
		t.Error("протухший heartbeat должен считаться offline")
	}
	var zero State
	if zero.Online(time.Minute) {
		t.Error("нулевой heartbeat (ещё ни разу) должен считаться offline")
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "нет.json")); err == nil {
		t.Fatal("ожидали ошибку чтения отсутствующего файла")
	}
}
