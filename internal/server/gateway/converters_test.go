package gateway

import (
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

func TestAdminStatusToProto(t *testing.T) {
	cases := []struct {
		in   string
		want pb.AdminAccessStatus
	}{
		{"pending", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_PENDING},
		{"approved", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_APPROVED},
		{"rejected", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_REJECTED},
		{"expired", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_EXPIRED},
		{"revoked", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_REVOKED},
		{"unknown", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_UNSPECIFIED},
		{"", pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := adminStatusToProto(c.in); got != c.want {
			t.Errorf("adminStatusToProto(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTriggerTypeToProto(t *testing.T) {
	cases := []struct {
		in   string
		want pb.ScriptTrigger
	}{
		{"schedule", pb.ScriptTrigger_SCRIPT_TRIGGER_SCHEDULE},
		{"event_trigger", pb.ScriptTrigger_SCRIPT_TRIGGER_EVENT},
		{"on_connect", pb.ScriptTrigger_SCRIPT_TRIGGER_ON_CONNECT},
		{"unknown", pb.ScriptTrigger_SCRIPT_TRIGGER_UNSPECIFIED},
		{"", pb.ScriptTrigger_SCRIPT_TRIGGER_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := triggerTypeToProto(c.in); got != c.want {
			t.Errorf("triggerTypeToProto(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEventNameToProto(t *testing.T) {
	cases := []struct {
		in   string
		want pb.ScriptEventType
	}{
		{"login", pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGIN},
		{"logout", pb.ScriptEventType_SCRIPT_EVENT_TYPE_LOGOUT},
		{"network_change", pb.ScriptEventType_SCRIPT_EVENT_TYPE_NETWORK_CHANGE},
		{"unknown", pb.ScriptEventType_SCRIPT_EVENT_TYPE_UNSPECIFIED},
		{"", pb.ScriptEventType_SCRIPT_EVENT_TYPE_UNSPECIFIED},
	}
	for _, c := range cases {
		if got := eventNameToProto(c.in); got != c.want {
			t.Errorf("eventNameToProto(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPlatformToInterpreter(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Windows", "powershell"},
		{"macOS", "shell"},
		{"linux", "shell"},
		{"", "shell"},
	}
	for _, c := range cases {
		if got := platformToInterpreter(c.in); got != c.want {
			t.Errorf("platformToInterpreter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
