// Command mockserver — ВРЕМЕННАЯ ЗАГЛУШКА под боевой сервер (Agent Gateway).
//
// ⚠️ THROWAWAY. Нужен только для локального end-to-end теста агента на Этапе 1.
// Использует ОБЩИЙ proto-пакет (github.com/Floodww/RoutineOps/proto) и
// ОБЩУЮ миграцию (migrations/001_initial_schema.sql, применяется docker-compose).
// Боевой сервер живёт в cmd/server + internal/server — это НЕ его прототип
// по архитектуре, только проверка контракта.
//
// Что делает:
//   - принимает Connect-стрим по mTLS;
//   - извлекает device_id из CN клиентского сертификата (ADR-1, будущий ADR-1
//     на стороне сервера);
//   - на каждый heartbeat обновляет devices.last_seen_at + ip, логирует "device X alive";
//   - фоновый job помечает устройства offline по таймауту last_seen
//     (alert agent_unreachable).
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var (
		listen         = flagEnv("listen", "MOCK_LISTEN", ":55443", "адрес прослушивания gRPC host:port")
		dsn            = flagEnv("db", "MOCK_DB_DSN", "postgres://mdm:mdm_dev_password@localhost:5432/mdm?sslmode=disable", "PostgreSQL DSN")
		certFile       = flagEnv("cert", "MOCK_SERVER_CERT", "certs/server.crt", "серверный сертификат")
		keyFile        = flagEnv("key", "MOCK_SERVER_KEY", "certs/server.key", "приватный ключ сервера")
		caFile         = flagEnv("ca", "MOCK_CA_CERT", "certs/ca.crt", "корневой CA для проверки клиентов")
		offlineTimeout = flag.Duration("offline-timeout", durEnv("MOCK_OFFLINE_TIMEOUT", 90*time.Second), "после какого молчания устройство считается offline")
		offlineScan    = flag.Duration("offline-scan", durEnv("MOCK_OFFLINE_SCAN", 30*time.Second), "период фоновой проверки offline")
		testTask       = flagEnv("test-task", "MOCK_TEST_TASK", "", "если задано — выслать одну тестовую задачу с этим script_content после подключения агента (для проверки Command Listener)")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// БД опциональна: без неё mock всё равно обслуживает протокол (heartbeat/задачи) —
	// удобно гонять Command Listener без Postgres. Запись в devices/alerts тогда пропускается.
	var pool *pgxpool.Pool
	if p, err := pgxpool.New(ctx, *dsn); err != nil {
		log.Warn("PostgreSQL недоступна — работаю без БД (только протокол)", slog.Any("error", err))
	} else if err := p.Ping(ctx); err != nil {
		log.Warn("PostgreSQL ping не прошёл — работаю без БД (только протокол)", slog.Any("error", err))
		p.Close()
	} else {
		pool = p
		defer pool.Close()
	}

	creds, err := serverCreds(*certFile, *keyFile, *caFile)
	if err != nil {
		log.Error("mTLS", slog.Any("error", err))
		os.Exit(1)
	}

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("listen", slog.String("addr", *listen), slog.Any("error", err))
		os.Exit(1)
	}

	grpcServer := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterAgentServiceServer(grpcServer, &mockServer{store: &store{pool: pool}, log: log, testTaskScript: *testTask})

	go runOfflineWatcher(ctx, &store{pool: pool}, *offlineTimeout, *offlineScan, log)

	go func() {
		<-ctx.Done()
		log.Info("остановка сервера")
		// Stop, а не GracefulStop: Connect — долгоживущий стрим, который сам не
		// завершается, и GracefulStop завис бы, ожидая его конца. Для throwaway-
		// заглушки жёсткая остановка корректна (и даёт агенту чистый обрыв).
		grpcServer.Stop()
	}()

	log.Info("mock-сервер слушает", slog.String("addr", *listen),
		slog.Duration("offline_timeout", *offlineTimeout))
	if err := grpcServer.Serve(lis); err != nil {
		log.Error("serve", slog.Any("error", err))
		os.Exit(1)
	}
}

// ---- gRPC ----

type mockServer struct {
	pb.UnimplementedAgentServiceServer
	store          *store
	log            *slog.Logger
	testTaskScript string // если задано — выслать одну тестовую задачу после подключения
}

func (s *mockServer) Connect(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) error {
	ctx := stream.Context()

	deviceID, fingerprint, err := deviceFromPeer(ctx)
	if err != nil {
		s.log.Warn("не удалось определить устройство из сертификата", slog.Any("error", err))
		return status.Error(codes.Unauthenticated, "cannot identify device from client certificate")
	}

	// Авто-регистрация (онбординг — Этап 3). Возвращает текущий статус устройства.
	deviceStatus, err := s.store.Register(ctx, deviceID, fingerprint)
	if err != nil {
		s.log.Error("регистрация устройства", slog.String("device_id", deviceID), slog.Any("error", err))
		return status.Error(codes.Internal, "register failed")
	}
	// Блокировка устройства (CONTEXT.md §8): отклоняем даже с валидным сертификатом.
	if deviceStatus == "blocked" {
		s.log.Warn("устройство заблокировано — соединение отклонено", slog.String("device_id", deviceID))
		return status.Error(codes.PermissionDenied, "device is blocked")
	}

	s.log.Info("устройство подключилось", slog.String("device_id", deviceID))

	// Тестовая задача для проверки Command Listener агента (throwaway, по флагу).
	if s.testTaskScript != "" {
		go s.sendTestTask(stream)
	}

	for {
		hb, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				s.log.Info("устройство отключилось", slog.String("device_id", deviceID))
				return nil
			}
			return err
		}
		if err := s.store.Heartbeat(ctx, deviceID, hb.GetIpAddress()); err != nil {
			s.log.Error("обновление last_seen", slog.String("device_id", deviceID), slog.Any("error", err))
			continue
		}
		s.log.Info("device alive", slog.String("device_id", deviceID), slog.String("ip", hb.GetIpAddress()))
	}
}

// sendTestTask шлёт одну задачу в стрим через 2с после подключения — чтобы
// прогнать полный цикл Command Listener агента без боевого сервера.
func (s *mockServer) sendTestTask(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) {
	time.Sleep(2 * time.Second)
	taskID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	task := &pb.Task{
		TaskId:        taskID,
		ScriptContent: s.testTaskScript,
		Platform:      "macOS",
		Priority:      pb.TaskPriority_TASK_PRIORITY_MEDIUM,
	}
	if err := stream.Send(task); err != nil {
		s.log.Error("не смог выслать тестовую задачу", slog.Any("error", err))
		return
	}
	s.log.Info("выслал тестовую задачу", slog.String("task_id", taskID), slog.String("script", s.testTaskScript))
}

// AckTaskReceived — агент подтвердил доставку задачи (до выполнения).
func (s *mockServer) AckTaskReceived(_ context.Context, in *pb.TaskReceivedAck) (*pb.TaskReceivedAckResponse, error) {
	s.log.Info("◀ ACK доставки задачи", slog.String("task_id", in.GetTaskId()), slog.Int64("received_at", in.GetReceivedAt()))
	return &pb.TaskReceivedAckResponse{Acknowledged: true}, nil
}

// ReportTaskResult — агент прислал результат выполнения.
func (s *mockServer) ReportTaskResult(_ context.Context, in *pb.TaskResult) (*pb.TaskResultAck, error) {
	s.log.Info("◀ РЕЗУЛЬТАТ задачи",
		slog.String("task_id", in.GetTaskId()),
		slog.String("status", in.GetStatus().String()),
		slog.String("output", strings.TrimSpace(in.GetOutput())),
		slog.String("error_log", strings.TrimSpace(in.GetErrorLog())))
	return &pb.TaskResultAck{Received: true}, nil
}

// ReportSecurityEvent — агент прислал алерт ИБ (Security Monitor).
func (s *mockServer) ReportSecurityEvent(_ context.Context, in *pb.SecurityEvent) (*pb.SecurityEventAck, error) {
	s.log.Warn("◀ АЛЕРТ ИБ",
		slog.String("type", in.GetAlertType().String()),
		slog.String("details", in.GetDetails()),
		slog.Int64("occurred_at", in.GetOccurredAt()))
	return &pb.SecurityEventAck{Received: true}, nil
}

// deviceFromPeer извлекает device_id (CN) и отпечаток клиентского сертификата
// из mTLS-соединения. Это будущий ADR-1 на стороне сервера.
func deviceFromPeer(ctx context.Context) (deviceID, fingerprint string, err error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", "", errors.New("no peer in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", "", errors.New("connection is not mTLS")
	}
	certs := tlsInfo.State.PeerCertificates
	if len(certs) == 0 {
		return "", "", errors.New("no client certificate")
	}
	cn := certs[0].Subject.CommonName
	if cn == "" {
		return "", "", errors.New("client certificate has empty CN")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return cn, hex.EncodeToString(sum[:]), nil
}

// ---- хранилище ----

type store struct {
	pool *pgxpool.Pool
}

// Register создаёт устройство при первом heartbeat (placeholder hostname/os —
// заполнятся на Этапе 2 через ReportInventory) и возвращает его статус.
func (s *store) Register(ctx context.Context, deviceID, fingerprint string) (string, error) {
	if s.pool == nil {
		return "active", nil // режим без БД — считаем устройство активным
	}
	const q = `
INSERT INTO devices (id, hostname, os, certificate_fingerprint, certificate_issued_at, last_seen_at, status)
VALUES ($1, 'unknown', 'unknown', $2, now(), now(), 'active')
ON CONFLICT (id) DO UPDATE SET certificate_fingerprint = EXCLUDED.certificate_fingerprint
RETURNING status;`
	var st string
	err := s.pool.QueryRow(ctx, q, deviceID, fingerprint).Scan(&st)
	return st, err
}

func (s *store) Heartbeat(ctx context.Context, deviceID, ip string) error {
	if s.pool == nil {
		return nil
	}
	const q = `UPDATE devices SET last_seen_at = now(), ip_address = $2 WHERE id = $1;`
	_, err := s.pool.Exec(ctx, q, deviceID, ip)
	return err
}

// markOffline заводит alert agent_unreachable по устройствам, молчавшим дольше
// timeout, — по одному разу на эпизод молчания. Возвращает их device_id.
func (s *store) markOffline(ctx context.Context, timeout time.Duration) ([]string, error) {
	if s.pool == nil {
		return nil, nil
	}
	const q = `
INSERT INTO alerts (device_id, alert_type, details)
SELECT d.id, 'agent_unreachable', $2
FROM devices d
WHERE d.last_seen_at IS NOT NULL
  AND d.last_seen_at < now() - make_interval(secs => $1)
  AND NOT EXISTS (
    SELECT 1 FROM alerts a
    WHERE a.device_id = d.id AND a.alert_type = 'agent_unreachable'
      AND a.created_at > d.last_seen_at
  )
RETURNING device_id;`
	rows, err := s.pool.Query(ctx, q, timeout.Seconds(), fmt.Sprintf("no heartbeat for > %s", timeout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func runOfflineWatcher(ctx context.Context, st *store, timeout, scan time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(scan)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := st.markOffline(ctx, timeout)
			if err != nil {
				if ctx.Err() == nil {
					log.Error("offline watcher", slog.Any("error", err))
				}
				continue
			}
			for _, id := range ids {
				log.Warn("device offline", slog.String("device_id", id))
			}
		}
	}
}

// ---- mTLS ----

func serverCreds(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no valid certs in %s", caFile)
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert, // mTLS: клиентский серт обязателен
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// ---- мелкие хелперы флагов/env ----

func flagEnv(name, envKey, def, usage string) *string {
	if v, ok := os.LookupEnv(envKey); ok && v != "" {
		def = v
	}
	return flag.String(name, def, fmt.Sprintf("%s (env %s)", usage, envKey))
}

func durEnv(envKey string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(envKey); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
