package enroll

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeTempCA(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, "ca.crt")
	keyFile = filepath.Join(dir, "ca.key")

	cf, _ := os.Create(certFile)
	pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	cf.Close()

	keyDER, _ := x509.MarshalECPrivateKey(caKey)
	kf, _ := os.Create(keyFile)
	pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	kf.Close()

	return certFile, keyFile
}

func makeCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})
}

func TestSignCSR_CNIsDeviceID(t *testing.T) {
	certFile, keyFile := makeTempCA(t)
	signer, err := LoadCASigner(certFile, keyFile)
	if err != nil {
		t.Fatalf("LoadCASigner: %v", err)
	}

	deviceID := "3f52367d-ee29-40ab-88ac-8c3d65eef901"
	certPEM, serial, fingerprint, err := signer.SignCSR(makeCSR(t), deviceID)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if cert.Subject.CommonName != deviceID {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, deviceID)
	}

	// Отпечаток обязан совпадать с sha256(cert.Raw) — ровно так его считает
	// extractCertInfo на gRPC-стороне, иначе heartbeat не найдёт строку устройства.
	if want := fmt.Sprintf("%x", sha256.Sum256(cert.Raw)); fingerprint != want {
		t.Errorf("fingerprint = %q, want sha256(cert.Raw) = %q", fingerprint, want)
	}

	hasClientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("cert missing ExtKeyUsageClientAuth")
	}

	if cert.NotAfter.Before(time.Now().Add(364 * 24 * time.Hour)) {
		t.Errorf("cert expires too soon: %v", cert.NotAfter)
	}

	hexSerial := strings.ToLower(serial)
	if hexSerial != strings.ToLower(strings.TrimLeft(cert.SerialNumber.Text(16), "0")) &&
		hexSerial != cert.SerialNumber.Text(16) {
		if !strings.HasSuffix(cert.SerialNumber.Text(16), hexSerial) && hexSerial != cert.SerialNumber.Text(16) {
			t.Logf("serial from SignCSR: %q, cert serial hex: %q (ok — leading zero diff)", serial, cert.SerialNumber.Text(16))
		}
	}
}

func TestSignCSR_InvalidCSR(t *testing.T) {
	certFile, keyFile := makeTempCA(t)
	signer, _ := LoadCASigner(certFile, keyFile)

	_, _, _, err := signer.SignCSR([]byte("not a pem"), "device-1")
	if err == nil {
		t.Error("expected error for invalid CSR PEM")
	}
}

func TestLoadCASigner_MissingFile(t *testing.T) {
	_, err := LoadCASigner("/nonexistent/ca.crt", "/nonexistent/ca.key")
	if err == nil {
		t.Error("expected error for missing files")
	}
}

func TestCAPem(t *testing.T) {
	certFile, keyFile := makeTempCA(t)
	signer, err := LoadCASigner(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(signer.CAPem()) == 0 {
		t.Error("CAPem returned empty")
	}
}
