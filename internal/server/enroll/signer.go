package enroll

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

type CASigner struct {
	cert  *x509.Certificate
	key   crypto.PrivateKey
	caPEM []byte
}

func LoadCASigner(certFile, keyFile string) (*CASigner, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load CA key pair: %w", err)
	}
	caCert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	caPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert file: %w", err)
	}
	return &CASigner{cert: caCert, key: pair.PrivateKey, caPEM: caPEM}, nil
}

func (s *CASigner) SignCSR(csrPEM []byte, deviceID string) (certPEM []byte, serial, fingerprint string, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, "", "", fmt.Errorf("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", "", fmt.Errorf("CSR signature invalid: %w", err)
	}

	serialNum, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", "", fmt.Errorf("generate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serialNum,
		Subject:      pkix.Name{CommonName: deviceID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		PublicKey:    csr.PublicKey,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, s.cert, csr.PublicKey, s.key)
	if err != nil {
		return nil, "", "", fmt.Errorf("sign certificate: %w", err)
	}
	// Отпечаток = sha256(DER) в hex — ровно как считает extractCertInfo на gRPC-
	// стороне (gateway.go), чтобы heartbeat ON CONFLICT(certificate_fingerprint)
	// попал в ТУ ЖЕ строку устройства, а не создал дубль (БАГ 4).
	fingerprint = fmt.Sprintf("%x", sha256.Sum256(certDER))
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		fmt.Sprintf("%x", serialNum.Bytes()), fingerprint, nil
}

func (s *CASigner) CAPem() []byte { return s.caPEM }
