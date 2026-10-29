package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// connectStub — AgentService с настраиваемым поведением Connect.
type connectStub struct {
	pb.UnimplementedAgentServiceServer
	onConnect func(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) error
}

func (s *connectStub) Connect(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) error {
	return s.onConnect(stream)
}

func writePEMt(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

func genCertsT(t *testing.T, dir string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	writePEMt(t, filepath.Join(dir, "ca.crt"), "CERTIFICATE", caDER)

	mk := func(name, cn string, serial int64, eku x509.ExtKeyUsage, dns []string, ips []net.IP) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
			DNSNames:     dns,
			IPAddresses:  ips,
		}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &k.PublicKey, caKey)
		writePEMt(t, filepath.Join(dir, name+".crt"), "CERTIFICATE", der)
		kder, _ := x509.MarshalPKCS8PrivateKey(k)
		writePEMt(t, filepath.Join(dir, name+".key"), "PRIVATE KEY", kder)
	}
	mk("server", "localhost", 2, x509.ExtKeyUsageServerAuth, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	mk("agent", "test-device", 3, x509.ExtKeyUsageClientAuth, nil, nil)
}

// startTLSServer поднимает mTLS gRPC-сервер со stub и возвращает Dialer к нему.
func startTLSServer(t *testing.T, stub pb.AgentServiceServer) *Dialer {
	t.Helper()
	dir := t.TempDir()
	genCertsT(t, dir)

	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterAgentServiceServer(gs, stub)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	dialer, err := NewDialer(lis.Addr().String(), "localhost", FileCertProvider{
		CertFile: filepath.Join(dir, "agent.crt"),
		KeyFile:  filepath.Join(dir, "agent.key"),
		CAFile:   filepath.Join(dir, "ca.crt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dialer
}

// connectAndServe успешно открывает Connect-стрим через настоящий mTLS и вызывает session.
func TestConnectAndServeSuccess(t *testing.T) {
	dialer := startTLSServer(t, &connectStub{
		onConnect: func(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) error {
			<-stream.Context().Done() // держим стрим открытым, пока клиент не закроет
			return nil
		},
	})
	c := New(dialer, discardLog())

	sessionCalled := make(chan struct{})
	err := c.connectAndServe(context.Background(), func(_ context.Context, stream Stream) error {
		close(sessionCalled)
		return nil // сразу завершаем сессию
	})
	if err != nil {
		t.Fatalf("connectAndServe вернул ошибку: %v", err)
	}
	select {
	case <-sessionCalled:
	default:
		t.Fatal("session не была вызвана")
	}
}

// Run уходит в blocked-ветку (реже повторяет), если сервер вернул PermissionDenied.
func TestRunBlockedPath(t *testing.T) {
	dialer := startTLSServer(t, &connectStub{
		onConnect: func(grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.Task]) error {
			return status.Error(codes.PermissionDenied, "device blocked")
		},
	})
	c := New(dialer, discardLog())
	c.SetBlockedRetry(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	// session читает из стрима — получит PermissionDenied от сервера и вернёт его.
	err := c.Run(ctx, func(_ context.Context, stream Stream) error {
		_, rerr := stream.Recv()
		return rerr
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидали DeadlineExceeded по завершении, got %v", err)
	}
}
