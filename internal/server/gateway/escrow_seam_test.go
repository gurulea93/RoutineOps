package gateway_test

import (
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Open-core: escrow-сервис не зарегистрирован (newGW не зовёт RegisterEscrowService)
// → EscrowRecoveryKey отвечает Unimplemented. extractCertInfo проходит (общий путь всех
// RPC остаётся free). Enterprise-поведение (реальный escrow) — в escrow_test.go.
func TestEscrowRecoveryKey_UnimplementedWithoutService(t *testing.T) {
	db := newDB(t)
	gw := newGW(t, db)
	ctx, _ := makeCertCtx(t, "escrow-oss")
	_, err := gw.EscrowRecoveryKey(ctx, &pb.EscrowRecoveryKeyRequest{RequestId: "x"})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("want Unimplemented in open-core, got %v", err)
	}
}
