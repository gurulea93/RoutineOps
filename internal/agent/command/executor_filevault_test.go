package command

import (
	"context"
	"errors"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

// fakeRevoker записывает вызовы RevokeAndShutdown и отдаёт настроенный результат.
type fakeRevoker struct {
	calls []string
	state pb.LockState
	err   error
}

func (f *fakeRevoker) RevokeAndShutdown(_ context.Context, requestID string) (pb.LockState, error) {
	f.calls = append(f.calls, requestID)
	return f.state, f.err
}

// lock_mode=FILEVAULT → ветвится на revoker, НЕ на locker. Executor ДЕЛЕГИРУЕТ
// весь отчёт RevokeAndShutdown'у (тот durably репортит и успех, и провал сам) —
// executor НЕ пере-репортит, иначе дубль аудита/алерта.
func TestHandle_FileVaultLock_CallsRevoker_DelegatesReport(t *testing.T) {
	fc := &fakeClient{}
	fl := &fakeLocker{}
	fr := &fakeRevoker{state: pb.LockState_LOCK_STATE_FILEVAULT_REVOKED}
	e, _ := newTestExecutor(t, fc)
	e.SetLocker(fl)
	e.SetFileVaultRevoker(fr)

	e.Submit(&pb.Task{TaskId: "t-fv", Lock: &pb.LockCommand{
		RequestId: "req-fv", LockMode: pb.LockMode_LOCK_MODE_FILEVAULT,
	}})
	e.Shutdown()

	if len(fr.calls) != 1 || fr.calls[0] != "req-fv" {
		t.Fatalf("expected RevokeAndShutdown called once with req-fv, got %v", fr.calls)
	}
	if len(fl.lockCalls()) != 0 || fl.unlocks != 0 {
		t.Fatalf("overlay locker must NOT be invoked for lock_mode=FILEVAULT, got locks=%v unlocks=%d", fl.lockCalls(), fl.unlocks)
	}
	rep := fc.lockReportsCopy()
	if len(rep) != 0 {
		t.Fatalf("executor must NOT re-report for FILEVAULT — RevokeAndShutdown owns durable reporting; got %v", rep)
	}
}

// lock_mode=UNSPECIFIED (fail-safe default) → overlay locker, НЕ revoker —
// даже если revoker сконфигурирован.
func TestHandle_UnspecifiedLockMode_UsesOverlayNotRevoker(t *testing.T) {
	fc := &fakeClient{}
	fl := &fakeLocker{}
	fr := &fakeRevoker{state: pb.LockState_LOCK_STATE_FILEVAULT_REVOKED}
	e, _ := newTestExecutor(t, fc)
	e.SetLocker(fl)
	e.SetFileVaultRevoker(fr)

	e.Submit(&pb.Task{TaskId: "t-overlay", Lock: &pb.LockCommand{RequestId: "req-1"}})
	e.Shutdown()

	if len(fr.calls) != 0 {
		t.Fatalf("revoker must NOT be called for default/unspecified lock_mode, got %v", fr.calls)
	}
	if len(fl.lockCalls()) != 1 {
		t.Fatalf("expected overlay Lock to be called, got %v", fl.lockCalls())
	}
}

// Без сконфигурированного revoker lock_mode=FILEVAULT отчитывается ошибкой, а
// НЕ тихо деградирует в overlay.
func TestHandle_FileVaultLock_NoRevoker_ReportsErrorNotOverlay(t *testing.T) {
	fc := &fakeClient{}
	fl := &fakeLocker{}
	e, _ := newTestExecutor(t, fc)
	e.SetLocker(fl) // SetFileVaultRevoker НЕ вызывали

	e.Submit(&pb.Task{TaskId: "t-fv", Lock: &pb.LockCommand{
		RequestId: "req-fv", LockMode: pb.LockMode_LOCK_MODE_FILEVAULT,
	}})
	e.Shutdown()

	if len(fl.lockCalls()) != 0 {
		t.Fatalf("must not silently fall back to overlay locker, got %v", fl.lockCalls())
	}
	rep := fc.lockReportsCopy()
	if len(rep) != 1 || rep[0].GetDetails() == "ok" || rep[0].GetDetails() == "" {
		t.Fatalf("expected an error detail when revoker is unconfigured, got %v", rep)
	}
	// Misbuild-отчёт ОБЯЗАН быть FILEVAULT_REVOKE_FAILED (сервер аудитит+алертит по
	// нему), а не UNSPECIFIED, который сервер молча дропает.
	if rep[0].GetState() != pb.LockState_LOCK_STATE_FILEVAULT_REVOKE_FAILED {
		t.Fatalf("no-revoker report State = %v, want FILEVAULT_REVOKE_FAILED", rep[0].GetState())
	}
}

// Ошибка revoke (ABORT одного из гардов) — executor НЕ пере-репортит: RevokeAndShutdown
// сам durably шлёт FILEVAULT_REVOKE_FAILED. Executor лишь логирует.
func TestHandle_FileVaultLock_RevokeError_DelegatedNotReReported(t *testing.T) {
	fc := &fakeClient{}
	fr := &fakeRevoker{err: errors.New("revoke ABORT — residual owner")}
	e, _ := newTestExecutor(t, fc)
	e.SetFileVaultRevoker(fr)

	e.Submit(&pb.Task{TaskId: "t-fv", Lock: &pb.LockCommand{
		RequestId: "req-fv", LockMode: pb.LockMode_LOCK_MODE_FILEVAULT,
	}})
	e.Shutdown()

	if len(fr.calls) != 1 {
		t.Fatalf("expected RevokeAndShutdown called once, got %v", fr.calls)
	}
	rep := fc.lockReportsCopy()
	if len(rep) != 0 {
		t.Fatalf("executor must NOT re-report on revoke error — RevokeAndShutdown owns the durable FILEVAULT_REVOKE_FAILED report; got %v", rep)
	}
}

// unlock=true игнорирует lock_mode (сервер всегда сбрасывает его в overlay
// при unlockDevice, но агент не должен полагаться на это — defensive).
func TestHandle_UnlockIgnoresLockMode(t *testing.T) {
	fc := &fakeClient{}
	fl := &fakeLocker{}
	fr := &fakeRevoker{state: pb.LockState_LOCK_STATE_FILEVAULT_REVOKED}
	e, _ := newTestExecutor(t, fc)
	e.SetLocker(fl)
	e.SetFileVaultRevoker(fr)

	e.Submit(&pb.Task{TaskId: "t-unlock", Lock: &pb.LockCommand{
		RequestId: "req-1", Unlock: true, LockMode: pb.LockMode_LOCK_MODE_FILEVAULT,
	}})
	e.Shutdown()

	if len(fr.calls) != 0 {
		t.Fatalf("revoker must not be called on unlock, got %v", fr.calls)
	}
	if fl.unlocks != 1 {
		t.Fatalf("expected overlay Unlock to be called, got %d", fl.unlocks)
	}
}
