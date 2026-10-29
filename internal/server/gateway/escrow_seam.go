package gateway

import (
	"context"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EscrowService — шов для FileVault recovery-escrow. Реализуется enterprise-оверлеем
// (internal/server/escrow, //go:build enterprise). Open-core НИКОГДА не регистрирует
// его → EscrowRecoveryKey отвечает Unimplemented. Ноль crypto/age в open-core.
type EscrowService interface {
	Store(ctx context.Context, certFingerprint string, req *pb.EscrowRecoveryKeyRequest) (*pb.EscrowRecoveryKeyResponse, error)
}

// RegisterEscrowService подключает enterprise-реализацию escrow. Зовётся только в
// enterprise-composition-root (cmd/server, //go:build enterprise) после лиц-гейта.
func (g *Gateway) RegisterEscrowService(svc EscrowService) { g.escrowSvc = svc }

// EscrowRecoveryKey — тонкий nil-guarded диспатчер. Обязан существовать во free
// (часть shared pb.AgentServiceServer). extractCertInfo — общий для всех RPC, free.
func (g *Gateway) EscrowRecoveryKey(ctx context.Context, req *pb.EscrowRecoveryKeyRequest) (*pb.EscrowRecoveryKeyResponse, error) {
	_, fingerprint, err := extractCertInfo(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "cert: %v", err)
	}
	if g.escrowSvc == nil {
		return nil, status.Errorf(codes.Unimplemented, "filevault escrow is an enterprise feature (not built)")
	}
	return g.escrowSvc.Store(ctx, fingerprint, req)
}
