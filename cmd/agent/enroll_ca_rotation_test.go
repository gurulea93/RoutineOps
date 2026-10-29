package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/config"
)

// rotCA — самоподписанный CA для тестов идемпотентности/переиздания.
type rotCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

// newRotCA рождает CA «как в проде», и обе детали здесь неслучайны:
//
//   - NotBefore = сейчас, БЕЗ бэкдейта — ровно так корень создаёт `openssl req -x509`
//     (scripts/gen-certs.sh). Раньше фикстура бэкдейтила его на час, и именно поэтому
//     тесты не видели, что verifyIdentityChain разваливается на CA моложе минуты:
//     leaf сервер бэкдейтит (signer.go), а корень — нет.
//   - KeyUsage = 0 — прод-CA родится без extfile именно таким, без keyUsage вообще.
//     Фикстура с KeyUsageCertSign была добрее прода: она бы не заметила проверку,
//     которая на реальном CA падает (CheckSignatureFrom к KeyUsage=0 лоялен).
//
// Фикстура, которая добрее продакшена, — это тест, который врёт.
func newRotCA(t *testing.T, cn string) *rotCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &rotCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issueIdentity выписывает клиентскую идентичность (leaf EKU=clientAuth), подписанную
// ca, раскладывает agent.crt/agent.key/ca.crt в dir и возвращает file-конфиг. caPEM —
// какой CA положить в ca.crt на диске (может отличаться от подписавшего: так тест
// воспроизводит устройство, у которого на диске лежит СТАРЫЙ CA).
func issueIdentity(t *testing.T, dir string, ca *rotCA, cn string, caPEM []byte) *config.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	cfg := &config.Config{
		CertSource: "file",
		CertFile:   filepath.Join(dir, "agent.crt"),
		KeyFile:    filepath.Join(dir, "agent.key"),
		CAFile:     filepath.Join(dir, "ca.crt"),
	}
	writeFile(t, cfg.CertFile, string(certPEM))
	if err := os.WriteFile(cfg.KeyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if caPEM != nil {
		writeFile(t, cfg.CAFile, string(caPEM))
	}
	return cfg
}

func pinOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func caServer(t *testing.T, bundle []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	}))
}

// TestReusableIdentityKeepsIdempotency — РЕГРЕССИЯ v22 «token already used»: при том же
// CA повторная установка обязана пропускать энроллмент (не гасить токен второй раз).
func TestReusableIdentityKeepsIdempotency(t *testing.T) {
	caV1 := newRotCA(t, "CA-v1")

	t.Run("CA только с диска", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || !reused || id != "device-abc" {
			t.Fatalf("ждали (device-abc,true,nil), got (%q,%v,%v)", id, reused, err)
		}
	})

	t.Run("свежий CA по -ca-url тот же самый", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
		srv := caServer(t, caV1.pem)
		defer srv.Close()
		cfg.CAURL, cfg.CASHA256 = srv.URL, pinOf(caV1.pem)
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || !reused || id != "device-abc" {
			t.Fatalf("ждали reuse, got (%q,%v,%v)", id, reused, err)
		}
	})

	t.Run("сеть недоступна → фолбэк на CA с диска", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
		// Порт 1 гарантированно не слушается — FetchPinnedCA упадёт по сети.
		cfg.CAURL, cfg.CASHA256 = "http://127.0.0.1:1/ca.crt", pinOf(caV1.pem)
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || !reused {
			t.Fatalf("офлайн-переустановка не должна падать, got (%q,%v,%v)", id, reused, err)
		}
	})

	t.Run("источника CA нет вовсе → ErrNoCASource → reuse", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", nil) // ca.crt на диск не пишем
		cfg.CAFile = filepath.Join(t.TempDir(), "нет-ca.crt")         // и путь несуществующий
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || !reused {
			t.Fatalf("без источника CA сохраняем прежнее поведение, got (%q,%v,%v)", id, reused, err)
		}
	})
}

// TestReusableIdentityForeignCA — ЯДРО БАГА 5: серт от СТАРОГО CA, а сервер доверяет
// новому. Молча пропускать энроллмент нельзя — служба вечно падала бы на mTLS.
func TestReusableIdentityForeignCA(t *testing.T) {
	caV1 := newRotCA(t, "CA-v1")
	caV2 := newRotCA(t, "CA-v2")

	t.Run("есть -token → полный энроллмент", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV2.pem) // на диске уже CA-v2
		cfg.EnrollToken = "fresh-token"
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || reused || id != "" {
			t.Fatalf("ждали полный enroll (\"\",false,nil), got (%q,%v,%v)", id, reused, err)
		}
	})

	t.Run("нет -token → внятная ошибка", func(t *testing.T) {
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV2.pem)
		cfg.EnrollToken = ""
		_, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err == nil || reused {
			t.Fatalf("ждали ошибку без токена, got reused=%v err=%v", reused, err)
		}
		if !strings.Contains(err.Error(), "CA") {
			t.Errorf("ошибка должна упоминать CA: %v", err)
		}
	})

	t.Run("свежий -ca-url важнее старого файла", func(t *testing.T) {
		// На диске лежит СТАРЫЙ ca.crt (CA-v1, которым серт и подписан!), но сервер
		// уже отдаёт CA-v2 — без приоритета -ca-url баг не детектится.
		cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
		cfg.EnrollToken = "fresh-token"
		srv := caServer(t, caV2.pem)
		defer srv.Close()
		cfg.CAURL, cfg.CASHA256 = srv.URL, pinOf(caV2.pem)
		id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
		if err != nil || reused || id != "" {
			t.Fatalf("свежий CA-v2 должен форсить enroll, got (%q,%v,%v)", id, reused, err)
		}
	})
}

// TestReusableIdentityPinMismatchFailsLoud — пин не сошёлся: молча откатываться на CA
// с диска нельзя (MITM/протухший -ca-sha256), оператор обязан увидеть ошибку.
func TestReusableIdentityPinMismatchFailsLoud(t *testing.T) {
	caV1 := newRotCA(t, "CA-v1")
	caV2 := newRotCA(t, "CA-v2")
	cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
	srv := caServer(t, caV2.pem)
	defer srv.Close()
	cfg.CAURL, cfg.CASHA256 = srv.URL, pinOf(caV1.pem) // пин от v1, сервер отдаёт v2
	if _, _, err := reusableIdentity(cfg, time.Now(), discardLogger()); err == nil {
		t.Fatal("несовпадение пина должно падать громко, а не откатываться на диск")
	}
}

// TestReusableIdentityBackwardClockSkew — РЕГРЕСС идемпотентности v22 под сдвигом часов
// назад: тот же CA, но часы устройства ушли раньше leaf.NotBefore (севший CMOS / откат
// снапшота VM). Сверка цепочки НЕ должна форсить полный энроллмент уже погашенным
// токеном — иначе сервер отвечает 401 и установка падает. Сверка издателя к часам
// устройства не обращается вовсе (enroll.LeafSignedByCA).
func TestReusableIdentityBackwardClockSkew(t *testing.T) {
	caV1 := newRotCA(t, "CA-v1")
	cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV1.pem)
	cfg.EnrollToken = "already-spent-token" // токен есть, но уже погашен на сервере
	// Часы устройства ушли на 2 часа назад — раньше leaf.NotBefore (issueIdentity ставит
	// NotBefore ≈ now-1мин). existingDeviceID смотрит только NotAfter, поэтому серт всё
	// ещё «не истёк», и идемпотентность обязана сохраниться.
	skewed := time.Now().Add(-2 * time.Hour)
	id, reused, err := reusableIdentity(cfg, skewed, discardLogger())
	if err != nil || !reused || id != "device-abc" {
		t.Fatalf("сдвиг часов назад не должен ломать идемпотентность, got (%q,%v,%v)", id, reused, err)
	}
}

// TestReusableIdentityAgainstFreshCA — зеркало предыдущего теста с другого края.
//
// Сервер бэкдейтит leaf на минуту (internal/server/enroll/signer.go), а корень —
// нет (`openssl req -x509`). Значит первые 60 секунд жизни CA leaf.NotBefore лежит
// РАНЬШЕ CA.NotBefore. Пока цепочка верифицировалась в момент leaf.NotBefore, корень
// был «ещё не действителен», и совершенно легитимная идентичность отвергалась:
// reusableIdentity либо падал («переустановите с -token»), либо шёл на полный
// энроллмент уже погашенным токеном → 401.
//
// Окно 60 секунд — на ВЫПИСКЕ, а не на проверке: leaf.NotBefore вшит в серт навсегда,
// так что серт, выданный в первую минуту жизни CA, отвергался бы при КАЖДОЙ повторной
// установке. Главный пострадавший — ротация CA с немедленным переэнроллментом парка.
//
// Проверка издателя обязана быть о подписи, а не о времени.
func TestReusableIdentityAgainstFreshCA(t *testing.T) {
	ca := newRotCA(t, "CA-fresh") // NotBefore = сейчас, как openssl
	cfg := issueIdentity(t, t.TempDir(), ca, "device-abc", ca.pem)
	cfg.EnrollToken = "already-spent-token"

	// leaf.NotBefore ≈ now-1мин, то есть РАНЬШЕ рождения корня — это норма, не аномалия.
	id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
	if err != nil || !reused || id != "device-abc" {
		t.Fatalf("CA моложе минуты не должен ломать идемпотентность, got (%q,%v,%v)", id, reused, err)
	}
}

// TestReusableIdentityFreshForeignCAStillRejected — страховка от того, что фикс выше
// не выродился в «принимаем всё подряд»: CA свежий, но ЧУЖОЙ. Идентичность обязана
// быть отвергнута, иначе mTLS не поднимется, а мы об этом узнаем только по вечно
// падающей службе.
func TestReusableIdentityFreshForeignCAStillRejected(t *testing.T) {
	caV1 := newRotCA(t, "CA-v1")
	caV2 := newRotCA(t, "CA-v2") // сервер переиздал CA; наш leaf подписан ещё v1
	cfg := issueIdentity(t, t.TempDir(), caV1, "device-abc", caV2.pem)
	cfg.EnrollToken = "fresh-token" // токен есть → ждём тихий форс полного энроллмента

	id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
	if err != nil || reused || id != "" {
		t.Fatalf("чужой CA обязан форсить полный энроллмент, got (%q,%v,%v)", id, reused, err)
	}
}

// TestReusableIdentityNoIdentity — серта нет: reuse=false, в сеть не ходим.
func TestReusableIdentityNoIdentity(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		CertSource: "file",
		CertFile:   filepath.Join(dir, "agent.crt"), // файлов нет
		KeyFile:    filepath.Join(dir, "agent.key"),
		CAFile:     filepath.Join(dir, "ca.crt"),
	}
	id, reused, err := reusableIdentity(cfg, time.Now(), discardLogger())
	if err != nil || reused || id != "" {
		t.Fatalf("без идентичности ждали (\"\",false,nil), got (%q,%v,%v)", id, reused, err)
	}
}
