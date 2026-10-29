package gateway_test

import (
	"context"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── ReportInventory ───────────────────────────────────────────────────────────

// TestReportInventory_DBError покрывает ветку ошибки UpsertInventory.
func TestReportInventory_DBError(t *testing.T) {
	gw := newGW(t, newDB(t))
	certCtx, _ := makeCertCtx(t, "device-inv-dberr")
	ctx, cancel := context.WithCancel(certCtx)
	cancel()

	_, err := gw.ReportInventory(ctx, &pb.InventoryReport{
		DeviceInfo: &pb.DeviceInfo{Hostname: "h", Os: "linux"},
	})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("want Internal, got %v", code)
	}
}

// ── ReportTaskResult ──────────────────────────────────────────────────────────

// TestReportTaskResult_DBError покрывает ветку ошибки CompleteTask.
// Невалидный UUID вызывает ошибку приведения типа в PostgreSQL.
func TestReportTaskResult_DBError(t *testing.T) {
	gw := newGW(t, newDB(t))
	certCtx, _ := makeCertCtx(t, "device-taskres-dberr")

	resp, err := gw.ReportTaskResult(certCtx, &pb.TaskResult{TaskId: "not-a-uuid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Received {
		t.Error("expected Received=false for invalid task ID")
	}
}

// ── FetchPolicy ───────────────────────────────────────────────────────────────

// TestFetchPolicy_DBError покрывает ветку ошибки FetchPolicyRules.
func TestFetchPolicy_DBError(t *testing.T) {
	gw := newGW(t, newDB(t))
	certCtx, _ := makeCertCtx(t, "device-pol-dberr")
	ctx, cancel := context.WithCancel(certCtx)
	cancel()

	_, err := gw.FetchPolicy(ctx, &pb.FetchPolicyRequest{})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("want Internal, got %v", code)
	}
}

// ── FetchScriptPolicies ───────────────────────────────────────────────────────

// TestFetchScriptPolicies_DBError покрывает ветку ошибки GetEffectiveScriptPoliciesForDevice.
func TestFetchScriptPolicies_DBError(t *testing.T) {
	gw := newGW(t, newDB(t))
	certCtx, _ := makeCertCtx(t, "device-sp-dberr")
	ctx, cancel := context.WithCancel(certCtx)
	cancel()

	_, err := gw.FetchScriptPolicies(ctx, &pb.FetchScriptPoliciesRequest{})
	if code := status.Code(err); code != codes.Internal {
		t.Errorf("want Internal, got %v", code)
	}
}

// ── ReportSecurityEvent ───────────────────────────────────────────────────────

// TestReportSecurityEvent_CreateAlertError покрывает ветку ошибки CreateAlert.
// Передаём невалидный UUID в AdminAccessRequestId — это вызывает ошибку
// приведения типа в PostgreSQL при вставке в UUID-колонку.
func TestReportSecurityEvent_CreateAlertError(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)

	certCtx, fingerprint := makeCertCtx(t, "device-secev-alerterr")
	registerDevice(t, db, "device-secev-alerterr", fingerprint)

	_, err := gw.ReportSecurityEvent(certCtx, &pb.SecurityEvent{
		AlertType:            pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE,
		Details:              "curl",
		AdminAccessRequestId: "not-a-uuid",
	})
	// Транзиентная ошибка БД → codes.Unavailable, чтобы агент ретраил из outbox (6d4a660).
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("want Unavailable when CreateAlert fails, got %v", code)
	}
}

// ── ReportAdminAccess ─────────────────────────────────────────────────────────

// TestReportAdminAccess_DBError покрывает ветку ошибки UpdateAdminAccessReport.
func TestReportAdminAccess_DBError(t *testing.T) {
	gw := newGW(t, newDB(t))
	certCtx, _ := makeCertCtx(t, "device-repaa-dberr")

	_, err := gw.ReportAdminAccess(certCtx, &pb.ReportAdminAccessRequest{
		RequestId: "not-a-uuid",
		Status:    pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_APPROVED,
	})
	// Невалидный UUID → реальная ошибка БД → codes.Unavailable (6d4a660).
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("want Unavailable for DB error, got %v", code)
	}
}

// TestReportAdminAccess_UnknownRequest: отчёт по несуществующей/закрытой заявке —
// accept-and-drop (Received=true), НЕ gRPC-ошибка. Иначе у агента FIFO-outbox
// встаёт на этой записи (poison pill).
func TestReportAdminAccess_UnknownRequest(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)
	certCtx, fingerprint := makeCertCtx(t, "device-repaa-unknown")
	registerDevice(t, db, "device-repaa-unknown", fingerprint)

	resp, err := gw.ReportAdminAccess(certCtx, &pb.ReportAdminAccessRequest{
		RequestId: "00000000-0000-0000-0000-000000000000",
		Status:    pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_REVOKED,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Received {
		t.Error("expected Received=true (accept-and-drop) for unknown/closed request")
	}
}

// ── ReportScriptResult ────────────────────────────────────────────────────────

// TestReportScriptResult_SaveError покрывает ветку ошибки SaveScriptResult.
// Передаём невалидный UUID в PolicyId — вызывает ошибку приведения типа в PostgreSQL.
func TestReportScriptResult_SaveError(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)

	certCtx, fingerprint := makeCertCtx(t, "device-scres-saverr")
	registerDevice(t, db, "device-scres-saverr", fingerprint)

	_, err := gw.ReportScriptResult(certCtx, &pb.ScriptResult{
		PolicyId: "not-a-uuid",
		RunId:    "not-a-uuid-either",
		ExitCode: 0,
	})
	// Транзиентная ошибка БД → codes.Unavailable, чтобы агент ретраил из outbox (6d4a660).
	if code := status.Code(err); code != codes.Unavailable {
		t.Errorf("want Unavailable when SaveScriptResult fails, got %v", code)
	}
}
