package gateway_test

import (
	"context"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// dropSetCodes — терминальные gRPC-коды, по которым агент ДРОПАЕТ запись из
// outbox (cmd/agent/main.go reportErr). Если хоть один из них вернётся на
// ТРАНЗИЕНТНОМ (восстановимом) условии, агент молча потеряет loss-sensitive
// отчёт: алерт ИБ / аудит прав / результат скрипта. Контракт: три отчётных RPC
// отдают эти коды только на ОКОНЧАТЕЛЬНОМ вердикте, никогда на транзиенте; на
// транзиенте — codes.Unavailable (агент ретраит из outbox).
// Контракт ack задокументирован в комментариях gateway.go.
var dropSetCodes = map[codes.Code]bool{
	codes.NotFound:           true,
	codes.InvalidArgument:    true,
	codes.FailedPrecondition: true,
}

// TestOutboxRPCs_TransientErrorNeverTerminalCode закрепляет инвариант
// poison-pill: на транзиентном условии (БД недоступна, lookup/insert/update
// упали) все три outbox-RPC возвращают codes.Unavailable, а НЕ терминальный код
// из dropSetCodes. Этот инвариант раньше держался только на конвенции/ревью —
// ни один тест его не пинговал.
//
// Самый коварный регресс, который ловит тест: переклейка ветки ошибки
// GetDeviceIDByFingerprint в ReportSecurityEvent/ReportScriptResult с
// codes.Unavailable на codes.NotFound «device not found». Условие там
// восстановимое (БД моргнула), но код стал бы терминальным → агент дропнул бы
// ИБ-событие/результат скрипта молча. Эти две ветки до сих пор не были покрыты
// (lookup-fail), поэтому такой рефактор прошёл бы зелёным.
func TestOutboxRPCs_TransientErrorNeverTerminalCode(t *testing.T) {
	// Каждый кейс приводит конкретный outbox-RPC к транзиентной ошибке и
	// возвращает gRPC-ошибку хендлера. Два способа инъекции, как в остальных
	// gateway-тестах: отменённый контекст (падает первый запрос к БД) и
	// невалидный UUID (cast-ошибка PostgreSQL на вставке/апдейте).
	cases := []struct {
		name string
		call func(t *testing.T) error
	}{
		{
			// lookup устройства падает (отменённый контекст) — ранее не покрыто.
			name: "ReportSecurityEvent/lookup-device",
			call: func(t *testing.T) error {
				gw := newGW(t, newDB(t))
				certCtx, _ := makeCertCtx(t, "drop-secev-lookup")
				ctx, cancel := context.WithCancel(certCtx)
				cancel()
				_, err := gw.ReportSecurityEvent(ctx, &pb.SecurityEvent{
					AlertType: pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE,
					Details:   "curl",
				})
				return err
			},
		},
		{
			// CreateAlert падает (невалидный UUID → cast-ошибка в PG).
			name: "ReportSecurityEvent/create-alert",
			call: func(t *testing.T) error {
				db := newDB(t)
				gw := newGW(t, db)
				certCtx, fingerprint := makeCertCtx(t, "drop-secev-alert")
				registerDevice(t, db, "drop-secev-alert", fingerprint)
				_, err := gw.ReportSecurityEvent(certCtx, &pb.SecurityEvent{
					AlertType:            pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE,
					Details:              "curl",
					AdminAccessRequestId: "not-a-uuid",
				})
				return err
			},
		},
		{
			// UpdateAdminAccessReport падает (невалидный UUID). Важно: статус
			// валиден (APPROVED), иначе сработала бы ШТАТНАЯ терминальная ветка
			// InvalidArgument до обращения к БД — а здесь проверяется транзиент.
			name: "ReportAdminAccess/update-report",
			call: func(t *testing.T) error {
				gw := newGW(t, newDB(t))
				certCtx, _ := makeCertCtx(t, "drop-repaa-update")
				_, err := gw.ReportAdminAccess(certCtx, &pb.ReportAdminAccessRequest{
					RequestId: "not-a-uuid",
					Status:    pb.AdminAccessStatus_ADMIN_ACCESS_STATUS_APPROVED,
				})
				return err
			},
		},
		{
			// lookup устройства падает (отменённый контекст) — ранее не покрыто.
			name: "ReportScriptResult/lookup-device",
			call: func(t *testing.T) error {
				gw := newGW(t, newDB(t))
				certCtx, _ := makeCertCtx(t, "drop-scres-lookup")
				ctx, cancel := context.WithCancel(certCtx)
				cancel()
				_, err := gw.ReportScriptResult(ctx, &pb.ScriptResult{
					PolicyId: "00000000-0000-0000-0000-000000000000",
					RunId:    "run-x",
				})
				return err
			},
		},
		{
			// SaveScriptResult падает (невалидный UUID в PolicyId).
			name: "ReportScriptResult/save-result",
			call: func(t *testing.T) error {
				db := newDB(t)
				gw := newGW(t, db)
				certCtx, fingerprint := makeCertCtx(t, "drop-scres-save")
				registerDevice(t, db, "drop-scres-save", fingerprint)
				_, err := gw.ReportScriptResult(certCtx, &pb.ScriptResult{
					PolicyId: "not-a-uuid",
					RunId:    "run-y",
				})
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t)
			if err == nil {
				t.Fatal("ожидалась транзиентная ошибка, получено nil " +
					"(агент счёл бы запись доставленной и удалил из outbox)")
			}
			code := status.Code(err)
			if dropSetCodes[code] {
				t.Fatalf("транзиентное условие вернуло терминальный код %v: агент ДРОПНЕТ "+
					"запись из outbox → молчаливая потеря loss-sensitive отчёта. "+
					"На транзиенте контракт требует codes.Unavailable", code)
			}
			if code != codes.Unavailable {
				t.Errorf("ожидался codes.Unavailable на транзиентном условии, получено %v", code)
			}
		})
	}
}
