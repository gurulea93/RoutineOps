package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/Floodww/RoutineOps/internal/agent/config"
	"github.com/Floodww/RoutineOps/internal/agent/transport"
)

// certExpiryWarnDays — порог, ниже которого diag помечает сертификат как близкий
// к истечению (нужно перевыпустить/перезаписаться, см. трек A2).
const certExpiryWarnDays = 14

// certInfo — извлечённые из сертификата поля для отчёта diag.
type certInfo struct {
	subjectCN string
	issuerCN  string
	notBefore time.Time
	notAfter  time.Time
}

// daysUntilExpiry возвращает целое число суток до истечения относительно now
// (отрицательное — сертификат уже истёк).
func (c certInfo) daysUntilExpiry(now time.Time) int {
	return int(c.notAfter.Sub(now).Hours() / 24)
}

// expired сообщает, истёк ли сертификат к моменту now. Через сравнение времени,
// а не daysUntilExpiry: серт, истёкший <24ч назад, даёт days==0 (усечение).
func (c certInfo) expired(now time.Time) bool {
	return now.After(c.notAfter)
}

// leafInfo извлекает поля leaf-сертификата из загруженного mTLS-материала.
func leafInfo(cert tls.Certificate) (certInfo, error) {
	if len(cert.Certificate) == 0 {
		return certInfo{}, fmt.Errorf("сертификат не содержит цепочки")
	}
	leaf := cert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return certInfo{}, fmt.Errorf("разбор leaf-сертификата: %w", err)
		}
		leaf = parsed
	}
	return certInfo{
		subjectCN: leaf.Subject.CommonName,
		issuerCN:  leaf.Issuer.CommonName,
		notBefore: leaf.NotBefore,
		notAfter:  leaf.NotAfter,
	}, nil
}

// caFileInfo парсит первый сертификат из PEM-файла CA.
func caFileInfo(path string) (certInfo, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return certInfo{}, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return certInfo{}, fmt.Errorf("в %s нет PEM-блока", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return certInfo{}, err
	}
	return certInfo{
		subjectCN: cert.Subject.CommonName,
		issuerCN:  cert.Issuer.CommonName,
		notBefore: cert.NotBefore,
		notAfter:  cert.NotAfter,
	}, nil
}

// expiryNote форматирует строку срока действия с пометкой WARN/EXPIRED.
func expiryNote(ci certInfo, now time.Time) string {
	days := ci.daysUntilExpiry(now)
	switch {
	case ci.expired(now):
		return fmt.Sprintf("ИСТЁК (%s)", ci.notAfter.Format(time.RFC3339))
	case days <= certExpiryWarnDays:
		return fmt.Sprintf("через %d дн — ВНИМАНИЕ, близко к истечению (%s)", days, ci.notAfter.Format(time.RFC3339))
	default:
		return fmt.Sprintf("через %d дн (%s)", days, ci.notAfter.Format(time.RFC3339))
	}
}

// fileState — однострочное состояние файла состояния агента.
func fileState(path string) string {
	if path == "" {
		return "(не задан)"
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("%s — отсутствует", path)
		}
		return fmt.Sprintf("%s — ошибка: %v", path, err)
	}
	return fmt.Sprintf("%s — %d Б, изменён %s", path, fi.Size(), fi.ModTime().Format(time.RFC3339))
}

// outboxState считает записи в каталоге устойчивой очереди.
func outboxState(dir string) string {
	if dir == "" {
		return "(не задан)"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("%s — отсутствует (пусто)", dir)
		}
		return fmt.Sprintf("%s — ошибка: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return fmt.Sprintf("%s — %d записей", dir, n)
}

// logCertHealth проверяет клиентский mTLS-серт при старте и пишет его состояние
// в лог службы — чтобы на реальной машине сразу было видно причину, если
// соединение не поднимается (истёкший серт, clock skew, нечитаемый файл), а не
// только молчаливый бэкофф реконнекта.
func logCertHealth(log *slog.Logger, provider transport.CertProvider, now time.Time) {
	cert, err := provider.ClientCertificate()
	if err != nil {
		log.Error("client cert: не удалось загрузить — mTLS не поднимется", slog.Any("error", err))
		return
	}
	ci, err := leafInfo(cert)
	if err != nil {
		log.Error("client cert: не удалось разобрать", slog.Any("error", err))
		return
	}
	days := ci.daysUntilExpiry(now)
	attrs := []any{
		slog.String("device_id", ci.subjectCN),
		slog.Int("days_left", days),
		slog.Time("not_after", ci.notAfter),
	}
	switch {
	case ci.expired(now):
		log.Error("client cert ИСТЁК — требуется перевыпуск/re-enroll", attrs...)
	case now.Before(ci.notBefore):
		// Серт ещё не действителен: вероятно рассинхрон часов на машине.
		log.Warn("client cert ещё не действителен (возможен сдвиг часов)",
			append(attrs, slog.Time("not_before", ci.notBefore))...)
	case days <= certExpiryWarnDays:
		log.Warn("client cert скоро истекает", attrs...)
	default:
		log.Info("client cert в порядке", attrs...)
	}
}

// runDiag печатает отчёт самодиагностики в w и возвращает код выхода:
// 0 — всё читается; 1 — клиентский серт нечитаем/истёк или --probe не прошёл.
// certs/probe инъектируются для тестируемости (probe == nil → проба пропускается).
func runDiag(w io.Writer, cfg *config.Config, certs transport.CertProvider, now time.Time, probe func() error) int {
	exit := 0

	fmt.Fprintf(w, "RoutineOps-agent diag\n")
	fmt.Fprintf(w, "  version:     %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(w, "  server:      %s (SNI %s)\n", cfg.ServerAddr, cfg.ServerName)
	fmt.Fprintf(w, "  cert-source: %s\n", cfg.CertSource)

	fmt.Fprintf(w, "\nclient cert:\n")
	tlsCert, err := certs.ClientCertificate()
	if err != nil {
		fmt.Fprintf(w, "  ОШИБКА загрузки: %v\n", err)
		exit = 1
	} else if ci, err := leafInfo(tlsCert); err != nil {
		fmt.Fprintf(w, "  ОШИБКА разбора: %v\n", err)
		exit = 1
	} else {
		fmt.Fprintf(w, "  subject:  CN=%s\n", ci.subjectCN)
		fmt.Fprintf(w, "  issuer:   CN=%s\n", ci.issuerCN)
		fmt.Fprintf(w, "  expires:  %s\n", expiryNote(ci, now))
		if ci.expired(now) {
			exit = 1
		}
	}

	fmt.Fprintf(w, "\nCA (%s):\n", cfg.CAFile)
	if ci, err := caFileInfo(cfg.CAFile); err != nil {
		fmt.Fprintf(w, "  ОШИБКА: %v\n", err)
	} else {
		fmt.Fprintf(w, "  subject:  CN=%s\n", ci.subjectCN)
		fmt.Fprintf(w, "  expires:  %s\n", expiryNote(ci, now))
	}

	fmt.Fprintf(w, "\nstate:\n")
	fmt.Fprintf(w, "  outbox:       %s\n", outboxState(cfg.OutboxDir))
	fmt.Fprintf(w, "  task-seen:    %s\n", fileState(cfg.TaskStateFile))
	fmt.Fprintf(w, "  script-seen:  %s\n", fileState(cfg.ScriptDedupFile))
	fmt.Fprintf(w, "  policy-cache: %s\n", fileState(cfg.ForbiddenListFile))

	// Секция ради одного сценария: службе прописан -update-url, а релизного ключа нет
	// → selfupdate не стартует, агент навсегда застревает на текущей версии, и на живой
	// машине это ничем не видно. Exit 1 — чтобы поломку ловил скрипт обхода флота
	// (`RoutineOps-agent diag`), а не человек глазами в логах. Без -update-url (dev-запуск,
	// ручной прогон) секция информационная и код выхода не трогает.
	fmt.Fprintf(w, "\nself-update:\n")
	if cfg.UpdateCheckURL == "" {
		fmt.Fprintf(w, "  не настроен (нет -update-url)\n")
	} else {
		fmt.Fprintf(w, "  manifest: %s\n", cfg.UpdateCheckURL)
		if ok, detail := updateKeyStatus(cfg); ok {
			fmt.Fprintf(w, "  ключ:     %s\n", detail)
		} else {
			fmt.Fprintf(w, "  ОШИБКА:   %s → обновления НЕ ставятся\n", detail)
			exit = 1
		}
	}

	if probe != nil {
		fmt.Fprintf(w, "\nprobe (mTLS dial %s):\n", cfg.ServerAddr)
		if err := probe(); err != nil {
			fmt.Fprintf(w, "  НЕУДАЧА: %v\n", err)
			exit = 1
		} else {
			fmt.Fprintf(w, "  OK\n")
		}
	}

	return exit
}
