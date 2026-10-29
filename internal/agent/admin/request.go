package admin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/transport"
	pb "github.com/Floodww/RoutineOps/proto"
)

// RequestAdmin отправляет заявку на временные права администратора (подкоманда
// `agent request-admin`). Печатает request_id и статус.
func RequestAdmin(ctx context.Context, dialer *transport.Dialer, reason string, log *slog.Logger) error {
	conn, err := dialer.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	resp, err := pb.NewAgentServiceClient(conn).RequestAdminAccess(ctx, &pb.RequestAdminAccessRequest{
		Reason:      reason,
		RequestedAt: time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	log.Info("заявка на права администратора создана",
		slog.String("request_id", resp.GetRequestId()),
		slog.String("status", resp.GetStatus().String()))
	fmt.Printf("request_id=%s status=%s\n", resp.GetRequestId(), resp.GetStatus())
	return nil
}
