// Package enroll реализует bootstrap-энроллмент агента (Этап 3): агент сам
// генерит ключевую пару и CSR, получает по одноразовому токену подписанный
// сервером сертификат (CN=device_id ставит сервер, ADR-1) и раскладывает
// mTLS-материал на диск. Контракт — docs/enrollment.md (REST POST /api/v1/enroll).
//
// Приватный ключ НИКОГДА не покидает устройство: сервер получает только CSR
// (публичный ключ). Канал энроллмента — server-auth TLS, доверие к серверу до
// получения CA обеспечивается CA-бандлом из установочного пакета (пин), запрос
// аутентифицируется одноразовым токеном.
package enroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Request — параметры энроллмента.
type Request struct {
	EnrollURL string // полный URL эндпоинта, напр. https://host:8081/api/v1/enroll
	Token     string // одноразовый enrollment-токен (UUID v4)
	CAFile    string // CA-бандл из пакета: пин TLS enroll-эндпоинта
	CAURL     string // откуда скачать CA-бандл, если CAFile нет на диске (TOFU)
	CASHA256  string // ожидаемый hex sha256 CA-бандла: пин против MITM на TOFU-шаге

	CertOut string // куда записать выданный сертификат (agent.crt)
	KeyOut  string // куда записать локально сгенерённый ключ (agent.key)
	CAOut   string // куда записать CA для рантайма (ca.crt)
	// ReleaseKeyOut — куда записать base64 release-pubkey из enroll-ответа (для
	// самообновления универсального агента без вшитого ключа). Пусто — не пишем.
	ReleaseKeyOut string

	Hostname string // справочно для UI
	OS       string
	Arch     string

	// HTTPClient — для тестов; в проде nil → клиент строится с пином CAFile.
	HTTPClient *http.Client
}

// enrollPayload — тело POST /api/v1/enroll (см. docs/enrollment.md).
type enrollPayload struct {
	EnrollmentToken string `json:"enrollment_token"`
	CSRPem          string `json:"csr_pem"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
}

// enrollResponse — ответ сервера.
type enrollResponse struct {
	DeviceID      string `json:"device_id"`
	CertPem       string `json:"cert_pem"`
	CAPem         string `json:"ca_pem"`
	ReleasePubKey string `json:"release_pubkey"` // base64 ed25519; self-update универсального агента
}

// Run выполняет энроллмент и раскладывает cert/key/ca на диск. Возвращает device_id.
func Run(ctx context.Context, req Request, log *slog.Logger) (string, error) {
	if req.EnrollURL == "" {
		return "", fmt.Errorf("не задан enroll-url")
	}
	if req.Token == "" {
		return "", fmt.Errorf("не задан токен энроллмента (-token)")
	}

	// 1. Локальная ключевая пара (ECDSA P-256) — приватный ключ не уходит наружу.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("генерация ключа: %w", err)
	}

	// 2. CSR с пустым subject — CN всё равно переопределит сервер (ADR-1).
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
		Subject:            pkix.Name{},
	}, key)
	if err != nil {
		return "", fmt.Errorf("создание CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	// 3. HTTP-клиент: пин CA из пакета (доверие к серверу до получения рантайм-CA).
	client := req.HTTPClient
	if client == nil {
		caPEM, lerr := loadCABundle(req.CAFile, req.CAURL, req.CASHA256)
		if lerr != nil {
			return "", lerr
		}
		client, err = pinnedClient(caPEM)
		if err != nil {
			return "", err
		}
	}

	// 4. POST enroll.
	body, _ := json.Marshal(enrollPayload{
		EnrollmentToken: req.Token,
		CSRPem:          string(csrPEM),
		Hostname:        req.Hostname,
		OS:              req.OS,
		Arch:            req.Arch,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.EnrollURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("запрос энроллмента: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // серт+CA — единицы КБ
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("сервер отклонил энроллмент: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}

	var er enrollResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return "", fmt.Errorf("разбор ответа сервера: %w", err)
	}
	if er.DeviceID == "" || er.CertPem == "" || er.CAPem == "" {
		return "", fmt.Errorf("неполный ответ сервера (device_id/cert_pem/ca_pem)")
	}

	// 5. Проверка: выданный серт подписан ИМЕННО под наш публичный ключ (сервер
	// подписал наш CSR, а не подсунул чужой серт). Дешёвая защита от путаницы.
	if err := certMatchesKey([]byte(er.CertPem), key); err != nil {
		return "", err
	}

	// 6. CA-бандл из ответа — будущий корень доверия рантайма (mTLS к серверу, проверка
	// сервера при self-update). Раньше он писался на диск как есть: проверок не было
	// вообще, поэтому лист-серт или протухший CA молча оседал в ca.crt, а агент потом
	// вечно падал на TLS-хендшейке с невнятной ошибкой вместо честного отказа здесь.
	// Хуже: CAOut == CAFile (см. cmd/agent/main.go), т.е. мусор затирал пиннутый CA из
	// пакета и сам становился пином для следующего запуска. Проверяем ДО записи, чтобы
	// при отказе на диске не осталось ни ключа, ни серта, ни битого CA.
	caCerts, err := validateCABundle([]byte(er.CAPem))
	if err != nil {
		return "", fmt.Errorf("CA-бандл из ответа сервера: %w", err)
	}
	warnIfCAExpiringSoon(caCerts, log)

	// 6б. Leaf и CA по отдельности валидны — но сходятся ли они? Если нет, mTLS не
	// поднимется, и узнаем мы об этом только по вечно падающей службе. Ловим здесь.
	if err := certChainsToCA([]byte(er.CertPem), caCerts); err != nil {
		return "", err
	}

	// 7. Раскладка на диск. Ключ — 0600 (секрет), серт/CA — 0644.
	keyPEM, err := encodeKey(key)
	if err != nil {
		return "", err
	}
	if err := writeFile(req.KeyOut, keyPEM, 0o600); err != nil {
		return "", fmt.Errorf("запись ключа: %w", err)
	}
	if err := writeFile(req.CertOut, []byte(er.CertPem), 0o644); err != nil {
		return "", fmt.Errorf("запись сертификата: %w", err)
	}
	if err := writeFile(req.CAOut, []byte(er.CAPem), 0o644); err != nil {
		return "", fmt.Errorf("запись CA: %w", err)
	}
	// Release-pubkey самообновления: универсальный агент без вшитого ключа берёт его
	// отсюда. Приходит по доверенному enroll-каналу (пин CA). Пусто — деплой без
	// self-update или сборка со вшитым ключом; молча пропускаем.
	if er.ReleasePubKey != "" && req.ReleaseKeyOut != "" {
		if err := writeFile(req.ReleaseKeyOut, []byte(er.ReleasePubKey), 0o644); err != nil {
			return "", fmt.Errorf("запись release-pubkey: %w", err)
		}
	}

	log.Info("энроллмент успешен",
		slog.String("device_id", er.DeviceID),
		slog.String("cert", req.CertOut),
		slog.String("key", req.KeyOut),
		slog.String("ca", req.CAOut),
	)
	return er.DeviceID, nil
}

// ErrNoCASource — источника CA нет вовсе: ни файла -ca на диске, ни -ca-url.
// Отдельная ошибка, чтобы вызывающий отличал «проверять нечем» от «проверили и
// не сошлось» (cmd/agent на первом сохраняет идемпотентность, на втором падает).
var ErrNoCASource = errors.New("для энроллмента нужен CA-бандл: задайте -ca <файл> или -ca-url <url>")

// ErrCAPinMismatch — бандл не сошёлся с -ca-sha256. Это не «сеть моргнула»: либо
// MITM, либо пин в параметрах установщика протух (CA переиздали, команду не
// обновили). Деградировать на CA с диска по этой ошибке нельзя.
var ErrCAPinMismatch = errors.New("CA-бандл не прошёл пин-проверку")

// loadCABundle возвращает CA-бандл для пина enroll-эндпоинта. Сначала читает файл
// caFile; если файла на диске нет ИЛИ он не сошёлся с пином, но задан caURL —
// скачивает бандл по URL (TOFU, как и серверный установщик-скрипт). Нужен хотя бы
// один источник: им проверяется TLS enroll-эндпоинта до получения рантайм-CA.
//
// Если задан caSHA256, полученный бандл (из файла ИЛИ по URL) сверяется с этим
// хешем и отвергается при несовпадении. Для caURL пин ОБЯЗАТЕЛЕН (см. ниже) — для
// caFile остаётся опциональной доп. проверкой (файл уже приходит из доверенного
// установочного пакета, TOFU по сети там не участвует).
func loadCABundle(caFile, caURL, caSHA256 string) ([]byte, error) {
	if caFile != "" {
		b, err := os.ReadFile(caFile)
		switch {
		case err == nil:
			pinned, perr := checkPin(b, caSHA256)
			if perr == nil {
				return pinned, nil
			}
			// Файл есть, но пину не соответствует. Раньше это был терминальный отказ — и
			// после ПЕРЕИЗДАНИЯ CA на сервере устройство залипало намертво: старый ca.crt
			// лежит на диске, новый пин его не принимает, а до -ca-url мы не доходили, так
			// что переэнроллиться было нечем (спасал только ручной rm). Если -ca-url задан,
			// тянем бандл заново: скачанное обязано сойтись с ТЕМ ЖЕ пином, доверие не
			// понижается (SEC-1 остаётся закрыт).
			if caURL == "" {
				return nil, perr
			}
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("чтение CA-бандла %s: %w", caFile, err)
		}
	}
	if caURL == "" {
		return nil, ErrNoCASource
	}
	// TOFU-скачивание по URL без пин-хеша НЕДОПУСТИМО (SEC-1, аудит 2026-07-01):
	// fetchCABundle сознательно пропускает проверку TLS (CA ещё не на диске), так
	// что MITM в момент установки подсунул бы свой CA — а дальнейший "пиннинг"
	// пиннил бы именно его. caSHA256 — единственная защита на этом шаге, поэтому
	// без него отказываем ДО сетевого запроса, а не молча доверяем TOFU.
	if caSHA256 == "" {
		return nil, fmt.Errorf("-ca-url задан без -ca-sha256: TOFU-скачивание CA без пин-хеша небезопасно (возможен MITM) — задайте -ca-sha256 либо разложите CA локальным файлом через -ca")
	}
	b, err := fetchCABundle(caURL)
	if err != nil {
		return nil, err
	}
	return checkPin(b, caSHA256)
}

// LoadCABundle — экспорт loadCABundle для cmd/agent: тем же бандлом, которым Run
// пинит enroll-запрос, проверяется и издатель уже лежащей на диске идентичности
// (иначе агент после переиздания CA молча оставил бы чужой серт).
func LoadCABundle(caFile, caURL, caSHA256 string) ([]byte, error) {
	return loadCABundle(caFile, caURL, caSHA256)
}

// FetchPinnedCA скачивает CA-бандл по URL и сверяет с пином. В отличие от
// LoadCABundle НЕ смотрит на диск: только так видно переиздание CA на сервере —
// лежащий на диске ca.crt остался бы старым и «подтвердил» бы старую идентичность.
func FetchPinnedCA(caURL, caSHA256 string) ([]byte, error) {
	if caURL == "" {
		return nil, ErrNoCASource
	}
	if caSHA256 == "" {
		return nil, fmt.Errorf("-ca-url задан без -ca-sha256: TOFU-скачивание CA без пин-хеша небезопасно (возможен MITM)")
	}
	b, err := fetchCABundle(caURL)
	if err != nil {
		return nil, err
	}
	return checkPin(b, caSHA256)
}

// checkPin сверяет sha256(b) с caSHA256, если он задан; пустой caSHA256 — пропуск
// проверки (допустимо только для caFile-источника, см. loadCABundle).
func checkPin(b []byte, caSHA256 string) ([]byte, error) {
	if caSHA256 == "" {
		return b, nil
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(caSHA256)) {
		return nil, fmt.Errorf("%w: sha256=%s, ожидался %s (возможен MITM)", ErrCAPinMismatch, got, caSHA256)
	}
	return b, nil
}

// caExpiryWarnWindow — за сколько до NotAfter корневого CA начинаем орать в логи.
// Протухший CA = мёртвый флот: агенты разом перестают доверять серверу, а понять
// причину по TLS-ошибке тяжело. Энроллмент — единственный момент, когда мы
// гарантированно смотрим на CA, поэтому предупреждаем сильно заранее.
const caExpiryWarnWindow = 30 * 24 * time.Hour

// validateCABundle разбирает CA-бандл и возвращает пригодные CA-сертификаты из него.
//
// Зачем отдельно от checkPin: checkPin отвечает на вопрос «те ли это байты»
// (целостность), а здесь — «а это вообще CA» (пригодность). Второго вопроса раньше не
// задавал никто: единственной проверкой был AppendCertsFromPEM, который принимает ЛЮБОЙ
// сертификат, включая обычный лист.
//
// Правила:
//   - бандл может быть цепочкой: лист и промежуточные соседствуют с корнем — это норма,
//     поэтому не-CA сертификаты не отвергаем, а просто не считаем корнями доверия;
//   - нужен хотя бы ОДИН пригодный CA, иначе доверять нечему → ошибка;
//   - keyUsage трактуем ровно как crypto/x509 при верификации цепочки: расширение
//     опционально (RFC 5280), и его ОТСУТСТВИЕ (KeyUsage == 0) значит «ограничений нет»,
//     а не «подписывать нельзя». Наш прод-CA рождается как `openssl req -x509` без
//     extfile и keyUsage не имеет ВОВСЕ — жёсткое требование CertSign отвергло бы
//     настоящий корневой CA и убило бы энроллмент всему флоту. Поэтому режем только
//     явный запрет: keyUsage задан, а бита CertSign в нём нет.
func validateCABundle(pemBytes []byte) ([]*x509.Certificate, error) {
	var (
		cas   []*x509.Certificate
		total int
	)
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue // ключи/параметры в бандле нас не интересуют
		}
		total++
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			// Битый блок — порча бандла, а не «лишний серт». Молчать нельзя:
			// AppendCertsFromPEM просто пропустил бы его, и причина отказа осталась бы
			// невыясненной.
			return nil, fmt.Errorf("сертификат #%d в бандле не разбирается: %w", total, err)
		}
		if !cert.IsCA {
			continue
		}
		if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			continue
		}
		cas = append(cas, cert)
	}

	if total == 0 {
		return nil, fmt.Errorf("в бандле нет ни одного PEM-блока CERTIFICATE")
	}
	if len(cas) == 0 {
		return nil, fmt.Errorf("в бандле %d сертификат(ов), но ни один не является CA "+
			"(нужен basicConstraints CA:TRUE и право подписи сертификатов) — похоже, вместо CA подсунут обычный лист-сертификат", total)
	}

	// Срок годности проверяем ПОСЛЕ отбора CA: бандл ротации законно содержит старый,
	// уже протухший корень рядом с новым. Это не повод отказывать — важно, чтобы остался
	// хотя бы один живой.
	now := time.Now()
	var live []*x509.Certificate
	var dead []*x509.Certificate
	for _, c := range cas {
		if now.Before(c.NotBefore) || now.After(c.NotAfter) {
			dead = append(dead, c)
			continue
		}
		live = append(live, c)
	}
	if len(live) == 0 {
		c := dead[0]
		return nil, fmt.Errorf("все CA в бандле недействительны по сроку: например %q действителен с %s по %s, а сейчас %s — проверьте часы устройства и срок жизни серверного CA",
			c.Subject.CommonName,
			c.NotBefore.UTC().Format(time.RFC3339),
			c.NotAfter.UTC().Format(time.RFC3339),
			now.UTC().Format(time.RFC3339))
	}
	return live, nil
}

// warnIfCAExpiringSoon предупреждает, что корень доверия скоро протухнет. Это ещё не
// ошибка (агент рабочий), но потом про истечение никто не вспомнит — пока весь флот
// разом не отвалится от сервера.
func warnIfCAExpiringSoon(cas []*x509.Certificate, log *slog.Logger) {
	deadline := time.Now().Add(caExpiryWarnWindow)
	for _, c := range cas {
		if c.NotAfter.Before(deadline) {
			log.Warn("CA скоро истекает — после этого агент перестанет доверять серверу",
				slog.String("ca_cn", c.Subject.CommonName),
				slog.Time("not_after", c.NotAfter),
				slog.Duration("remaining", time.Until(c.NotAfter)),
			)
		}
	}
}

// fetchCABundle скачивает CA-бандл по URL. Это TOFU-шаг: CA ещё нет на диске,
// поэтому TLS-верификацию пропускаем. Все последующие запросы (enroll, gRPC)
// идут через pinnedClient, который доверяет ТОЛЬКО скачанному здесь CA.
func fetchCABundle(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	tofuClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // TOFU: CA ещё нет
		},
	}
	resp, err := tofuClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("скачивание CA-бандла %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание CA-бандла %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("чтение CA-бандла %s: %w", url, err)
	}
	return b, nil
}

// pinnedClient строит HTTPS-клиент, доверяющий ТОЛЬКО переданному CA-бандлу.
func pinnedClient(pemBytes []byte) (*http.Client, error) {
	// Пин отвечает за «те ли это байты», а не «CA ли это»: AppendCertsFromPEM съест и
	// обычный лист-серт, и тогда клиент «успешно» построится на бандле, которым ничего
	// нельзя проверить, — падать будет позже и непонятно. Проверяем пригодность здесь.
	if _, err := validateCABundle(pemBytes); err != nil {
		return nil, fmt.Errorf("CA-бандл для пина enroll-эндпоинта: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("в CA-бандле нет валидных сертификатов")
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
		},
	}, nil
}

// certMatchesKey проверяет, что публичный ключ выданного серта совпадает с нашим.
func certMatchesKey(certPEM []byte, key *ecdsa.PrivateKey) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("серт от сервера не PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("разбор серта от сервера: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return fmt.Errorf("выданный серт не соответствует нашему ключу (сервер подписал чужой CSR?)")
	}
	return nil
}

// certChainsToCA проверяет, что ВЫДАННЫЙ leaf подписан CA из того же ответа.
//
// certMatchesKey сверяет leaf с нашим ключом, validateCABundle — что CA сам по себе
// цел. Но по отдельности оба могут быть валидны, а вместе не сходиться: сервер с
// рассинхроном после переиздания CA отдаёт leaf от СТАРОГО CA и новый ca_pem. Агент
// такое принимал, писал на диск и рапортовал «энроллмент успешен» — а служба потом
// вечно падала на mTLS-хендшейке. Это ровно тот fail-late, против которого писалась
// валидация CA: ловим здесь, до записи на диск.
//
// Часы в этой проверке НЕ участвуют вовсе — и это важно, тут легко ошибиться.
// Вопрос «подписан ли leaf этим CA» к времени отношения не имеет, а x509.Verify
// проверяет окна валидности ВСЕЙ цепочки, включая корень. Любой выбор CurrentTime
// ломается: time.Now() валит энроллмент на устройстве со съехавшими назад часами
// (севший CMOS, откат снапшота VM), а leaf.NotBefore валит его на свежем CA —
// сервер бэкдейтит leaf на минуту (internal/server/enroll/signer.go), а `openssl
// req -x509` (scripts/gen-certs.sh) корень не бэкдейтит, поэтому первые 60 секунд
// жизни CA корень «ещё не действителен» на момент leaf.NotBefore. Это ломало бы
// ровно те сценарии, ради которых проверка и писалась: ротацию CA с немедленным
// переэнроллментом флота, а ещё CI/e2e/демо-стенды, где CA рождается и тут же
// используется.
//
// Поэтому сверяем подпись напрямую: CheckSignatureFrom проверяет CA-ность родителя
// (BasicConstraints/IsCA), его право подписывать (KeyUsage, причём лояльно к
// KeyUsage=0 — а прод-CA рождается без extfile именно таким) и саму криптоподпись.
// Срок годности CA уже проверил validateCABundle по реальным часам, до нас.
//
// EKU=ClientAuth сверяем явно: сервер-подписчик ставит именно его, и это не даёт
// принять за клиентскую идентичность серверный или CA-серт.
func certChainsToCA(certPEM []byte, caCerts []*x509.Certificate) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("серт от сервера не PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("разбор серта от сервера: %w", err)
	}
	return LeafSignedByCA(leaf, caCerts)
}

// LeafSignedByCA — ядро проверки «этот leaf выписан этим CA»: подпись + EKU, без часов.
// Всё «почему без часов» — в доке certChainsToCA выше; коротко: любой выбор CurrentTime
// ломается (time.Now() — на съехавших назад часах, leaf.NotBefore — на CA моложе минуты).
//
// Экспортировано, потому что ровно та же проверка нужна cmd/agent для УЖЕ УСТАНОВЛЕННОЙ
// идентичности (reusableIdentity): вопрос там тот же, и второй реализации у него быть
// не должно — своя копия уже разошлась с этой и принесла ту самую ошибку с часами.
func LeafSignedByCA(leaf *x509.Certificate, caCerts []*x509.Certificate) error {
	clientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth || eku == x509.ExtKeyUsageAny {
			clientAuth = true
			break
		}
	}
	if !clientAuth {
		return fmt.Errorf("выданный серт не для клиентской аутентификации " +
			"(нет EKU clientAuth — сервер отдал не тот сертификат)")
	}

	for _, ca := range caCerts {
		if err := leaf.CheckSignatureFrom(ca); err == nil {
			return nil
		}
	}
	return fmt.Errorf("выданный серт не подписан CA из того же ответа " +
		"(рассинхрон CA на сервере — mTLS не поднимется)")
}

// ValidateCABundle — экспорт validateCABundle для cmd/agent: CA, дотянутый по
// -ca-url на reused-пути (relocateForService), проходит ту же проверку пригодности
// («это вообще CA?»), что и бандл на свежем энроллменте. Пин отвечает только за
// целостность байт — лист-сертификат вместо CA он не поймает.
func ValidateCABundle(pemBytes []byte) ([]*x509.Certificate, error) {
	return validateCABundle(pemBytes)
}

// ParseCACerts разбирает CA-бандл в сертификаты БЕЗ проверки сроков: единственный
// потребитель — сверка издателя (LeafSignedByCA), которой часы противопоказаны.
// Срок годности CA проверяет validateCABundle на пути энроллмента.
func ParseCACerts(pemBytes []byte) ([]*x509.Certificate, error) {
	var cas []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("разбор CA-сертификата: %w", err)
		}
		cas = append(cas, c)
	}
	if len(cas) == 0 {
		return nil, fmt.Errorf("в CA-бандле нет валидных сертификатов")
	}
	return cas, nil
}

// encodeKey сериализует приватный ключ в PKCS#8 PEM.
func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("сериализация ключа: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// writeFile создаёт родительский каталог и пишет файл с нужными правами.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, perm)
}
