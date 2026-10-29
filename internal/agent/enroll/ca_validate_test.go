package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSignedPEM лепит самоподписанный сертификат по «скелету» и возвращает его PEM.
// NotBefore/NotAfter/IsCA/KeyUsage задаёт вызывающий — этим и различаются кейсы.
func selfSignedPEM(t *testing.T, tmpl *x509.Certificate) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// caTmpl — «скелет» CA-сертификата. keyUsage=0 воспроизводит настоящий прод-CA
// (openssl req -x509 без extfile keyUsage не ставит вовсе).
func caTmpl(cn string, notBefore, notAfter time.Time, keyUsage x509.KeyUsage) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              keyUsage,
		BasicConstraintsValid: true,
	}
}

// TestValidateCABundleAcceptsCAWithoutKeyUsage — регрессия против «строгой» проверки
// из ТЗ: настоящий прод-CA (IsCA=true, KeyUsage=0) ОБЯЗАН приниматься. Если кто-то
// ужесточит до KeyUsage&CertSign — тест упадёт и спасёт флот.
func TestValidateCABundleAcceptsCAWithoutKeyUsage(t *testing.T) {
	pemBytes := selfSignedPEM(t, caTmpl("RoutineOps Root CA", time.Now().Add(-time.Hour), time.Now().Add(3650*24*time.Hour), 0))
	cas, err := validateCABundle(pemBytes)
	if err != nil {
		t.Fatalf("CA без keyUsage должен приниматься: %v", err)
	}
	if len(cas) != 1 {
		t.Fatalf("ждали 1 CA, got %d", len(cas))
	}
}

// TestValidateCABundleRejectsLeafOnly — ядро бага: самоподписанный лист (IsCA=false)
// не CA и не корень доверия.
func TestValidateCABundleRejectsLeafOnly(t *testing.T) {
	leaf := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "not-a-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true, // CA:FALSE
	}
	if _, err := validateCABundle(selfSignedPEM(t, leaf)); err == nil {
		t.Fatal("лист-сертификат не должен приниматься за CA")
	}
}

// TestValidateCABundleRejectsCertSignForbidden — IsCA=true, но keyUsage задан ЯВНО
// и без CertSign → подписывать сертификаты запрещено.
func TestValidateCABundleRejectsCertSignForbidden(t *testing.T) {
	// keyUsage выставлен (digitalSignature), но БЕЗ certSign — явный запрет.
	pemBytes := selfSignedPEM(t, caTmpl("weird", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.KeyUsageDigitalSignature))
	if _, err := validateCABundle(pemBytes); err == nil {
		t.Fatal("CA с keyUsage без certSign должен отвергаться")
	}
}

// TestValidateCABundleRejectsExpiredCA — честная ошибка на enroll вместо мёртвого флота.
func TestValidateCABundleRejectsExpiredCA(t *testing.T) {
	pemBytes := selfSignedPEM(t, caTmpl("old", time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour), 0))
	if _, err := validateCABundle(pemBytes); err == nil {
		t.Fatal("протухший CA должен отвергаться")
	}
}

// TestValidateCABundleRejectsNotYetValidCA — уехавшие в будущее часы устройства.
func TestValidateCABundleRejectsNotYetValidCA(t *testing.T) {
	pemBytes := selfSignedPEM(t, caTmpl("future", time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour), 0))
	if _, err := validateCABundle(pemBytes); err == nil {
		t.Fatal("ещё-не-действительный CA должен отвергаться")
	}
}

// TestValidateCABundleAcceptsChainWithLeaf — бандл «лист + корень» валиден, корнем
// доверия считается только root.
func TestValidateCABundleAcceptsChainWithLeaf(t *testing.T) {
	leaf := selfSignedPEM(t, &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "leaf"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
	})
	root := selfSignedPEM(t, caTmpl("root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0))
	cas, err := validateCABundle(append(append([]byte{}, leaf...), root...))
	if err != nil {
		t.Fatalf("бандл лист+корень должен приниматься: %v", err)
	}
	if len(cas) != 1 || cas[0].Subject.CommonName != "root" {
		t.Fatalf("корнем доверия должен быть только root, got %+v", cas)
	}
}

// TestValidateCABundleAcceptsRotationBundle — «протухший корень + живой корень»
// принимается, возвращается только живой.
func TestValidateCABundleAcceptsRotationBundle(t *testing.T) {
	dead := selfSignedPEM(t, caTmpl("old-root", time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour), 0))
	live := selfSignedPEM(t, caTmpl("new-root", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 0))
	cas, err := validateCABundle(append(append([]byte{}, dead...), live...))
	if err != nil {
		t.Fatalf("бандл ротации должен приниматься: %v", err)
	}
	if len(cas) != 1 || cas[0].Subject.CommonName != "new-root" {
		t.Fatalf("должен остаться только живой корень, got %+v", cas)
	}
}

// TestValidateCABundleRejectsGarbage — не-PEM и битый CERTIFICATE-блок отвергаются.
func TestValidateCABundleRejectsGarbage(t *testing.T) {
	if _, err := validateCABundle([]byte("совсем не PEM")); err == nil {
		t.Fatal("мусор без PEM-блоков должен отвергаться")
	}
	fake := []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n")
	if _, err := validateCABundle(fake); err == nil {
		t.Fatal("битый CERTIFICATE-блок должен отвергаться")
	}
}

// TestEnrollRejectsLeafCAInResponse — E2E: сервер отдаёт ВАЛИДНЫЙ агентский серт
// (certMatchesKey проходит), но в ca_pem кладёт лист. Run обязан упасть, и на диске
// не должно остаться ни ключа, ни серта, ни ca.crt.
func TestEnrollRejectsLeafCAInResponse(t *testing.T) {
	ca := newTestCA(t)
	leafCA := selfSignedPEM(t, &x509.Certificate{
		SerialNumber:          big.NewInt(9),
		Subject:               pkix.Name{CommonName: "leaf-not-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p enrollPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		certPEM := ca.signCSR(t, p.CSRPem, "dev-leaf")
		json.NewEncoder(w).Encode(enrollResponse{DeviceID: "dev-leaf", CertPem: string(certPEM), CAPem: string(leafCA)})
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := Run(context.Background(), Request{
		EnrollURL:  srv.URL,
		Token:      "x",
		CertOut:    filepath.Join(dir, "agent.crt"),
		KeyOut:     filepath.Join(dir, "agent.key"),
		CAOut:      filepath.Join(dir, "ca.crt"),
		HTTPClient: srv.Client(),
	}, discardLog())
	if err == nil {
		t.Fatal("ожидали отказ: в ca_pem лист вместо CA")
	}
	if !strings.Contains(err.Error(), "CA-бандл") {
		t.Errorf("ошибка не про CA-бандл: %v", err)
	}
	for _, name := range []string{"agent.key", "agent.crt", "ca.crt"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Errorf("при отказе %s не должен появиться на диске", name)
		}
	}
}

// TestPinnedClientRejectsLeafBundle — pinnedClient не строится на лист-сертификате.
func TestPinnedClientRejectsLeafBundle(t *testing.T) {
	leaf := selfSignedPEM(t, &x509.Certificate{
		SerialNumber:          big.NewInt(11),
		Subject:               pkix.Name{CommonName: "leaf"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
	})
	if _, err := pinnedClient(leaf); err == nil {
		t.Fatal("pinnedClient не должен строиться на лист-сертификате")
	}
}
