package gateway_test

import (
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Терминальная пара к gateway_dropset_test.go: отчёт ссылается на УДАЛЁННУЮ строку
// (FK 23503). Payload заморожен в outbox агента — ретрай не пройдёт никогда, поэтому
// по ack-контракту сервер обязан ответить Received:true (accept-and-drop), а НЕ
// Unavailable. Регресс наблюдался живьём: outbox тест-устройства вечно ретраил результаты
// удалённой script-политики.
func TestReportScriptResult_DeletedPolicyAcceptAndDrop(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)
	cn := "fkdrop-script"
	ctx, fp := makeCertCtx(t, cn)
	registerDevice(t, db, cn, fp)

	// Валидный UUID несуществующей (удалённой) политики → FK 23503 на INSERT.
	ack, err := gw.ReportScriptResult(ctx, &pb.ScriptResult{
		PolicyId: "11111111-1111-1111-1111-111111111111",
		RunId:    "fkdrop-run-1",
		ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("удалённая политика должна дать accept-and-drop, получена ошибка (агент "+
			"ретраил бы вечно — poison pill): %v", err)
	}
	if !ack.Received {
		t.Fatal("ожидался Received:true (accept-and-drop), иначе агент ретраит из outbox")
	}
}

func TestReportSecurityEvent_DeletedAdminRequestAcceptAndDrop(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)
	cn := "fkdrop-secev"
	ctx, fp := makeCertCtx(t, cn)
	registerDevice(t, db, cn, fp)

	// Валидный UUID несуществующей (вычищенной retention'ом) заявки → FK 23503.
	ack, err := gw.ReportSecurityEvent(ctx, &pb.SecurityEvent{
		AlertType:            pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE,
		Details:              "curl",
		AdminAccessRequestId: "22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("удалённая заявка должна дать accept-and-drop, получена ошибка: %v", err)
	}
	if !ack.Received {
		t.Fatal("ожидался Received:true (accept-and-drop), иначе агент ретраит из outbox")
	}
}
