//go:build screen

package screen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Floodww/RoutineOps/internal/server/storage"
)

// Загрузка Windows-агента с VP8 из панели (только суперадмин из SEED_ADMIN_EMAIL).
//
// Файл НЕ публикуется здесь: подпись релиза делает install.sh ключом release_ed25519.pem,
// который в контейнер сервера сознательно не монтируется (украденный из выставленного наружу
// сервера ключ подписи = захват всего парка). Сервер только кладёт три файла в BuildsDir
// (build/windows), откуда install.sh их берёт, если версия и sha256 совпали.

const (
	vpxBinName     = "agent_windows_amd64_vpx.exe"
	maxVPXBuild    = 300 << 20
	minVPXBuild    = 1 << 20
	uploadDeadline = 15 * time.Minute
)

var buildVersionRe = regexp.MustCompile(`^v?(\d{1,4}\.\d{1,4}\.\d{1,4})$`)

func (s *Service) isBuildAdmin(email string) bool {
	return s.cfg.BuildAdminEmail != "" && strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(s.cfg.BuildAdminEmail))
}

type vpxBuildInfo struct {
	Present  bool       `json:"present"`
	Version  string     `json:"version,omitempty"`
	SHA256   string     `json:"sha256,omitempty"`
	Bytes    int64      `json:"bytes,omitempty"`
	Uploaded *time.Time `json:"uploaded_at,omitempty"`
}

func (s *Service) buildsDir() string {
	if s.cfg.BuildsDir != "" {
		return s.cfg.BuildsDir
	}
	return "build/windows"
}

// httpVPXBuildInfo — GET /agent-builds/windows-vpx: что сейчас лежит на сервере. Не-суперадмину
// 403: по этому ответу панель решает, показывать ли карточку.
func (s *Service) httpVPXBuildInfo(w http.ResponseWriter, r *http.Request) {
	_, _, email, ok := actor(w, r)
	if !ok {
		return
	}
	if !s.isBuildAdmin(email) {
		writeErr(w, http.StatusForbidden, "super_admin_required")
		return
	}
	dir := s.buildsDir()
	info := vpxBuildInfo{}
	if st, err := os.Stat(filepath.Join(dir, vpxBinName)); err == nil {
		t := st.ModTime()
		info.Present, info.Bytes, info.Uploaded = true, st.Size(), &t
		if b, err := os.ReadFile(filepath.Join(dir, vpxBinName+".version")); err == nil {
			info.Version = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile(filepath.Join(dir, vpxBinName+".sha256")); err == nil {
			info.SHA256, _, _ = strings.Cut(strings.TrimSpace(string(b)), " ")
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// httpVPXBuildUpload — POST /agent-builds/windows-vpx (multipart: version, file).
// Поле version обязано идти ДО file: файл читается потоком, без буфера в памяти.
func (s *Service) httpVPXBuildUpload(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, email, ok := actor(w, r)
	if !ok {
		return
	}
	if !s.isBuildAdmin(email) {
		writeErr(w, http.StatusForbidden, "super_admin_required")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadDeadline))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadDeadline))
	r.Body = http.MaxBytesReader(w, r.Body, maxVPXBuild+1<<20)

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "multipart_required")
		return
	}
	var version string
	var tmpPath, sum string
	var size int64
	dir := s.buildsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.internal(w, "builds dir", err)
		return
	}
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_upload")
			return
		}
		switch part.FormName() {
		case "version":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			m := buildVersionRe.FindStringSubmatch(strings.TrimSpace(string(b)))
			if m == nil {
				writeErr(w, http.StatusBadRequest, "bad_version")
				return
			}
			version = "v" + m[1]
		case "file":
			if version == "" {
				writeErr(w, http.StatusBadRequest, "version_before_file")
				return
			}
			f, err := os.CreateTemp(dir, ".upload-*")
			if err != nil {
				s.internal(w, "temp file", err)
				return
			}
			tmpPath = f.Name()
			h := sha256.New()
			head := make([]byte, 2)
			n, _ := io.ReadFull(part, head)
			if n < 2 || head[0] != 'M' || head[1] != 'Z' {
				_ = f.Close()
				writeErr(w, http.StatusBadRequest, "not_a_windows_executable")
				return
			}
			_, _ = f.Write(head)
			h.Write(head)
			c, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(part, maxVPXBuild))
			size = c + 2
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				writeErr(w, http.StatusBadRequest, "upload_interrupted")
				return
			}
			if size < minVPXBuild || size >= maxVPXBuild {
				writeErr(w, http.StatusBadRequest, "bad_size")
				return
			}
			sum = hex.EncodeToString(h.Sum(nil))
		}
	}
	if version == "" || tmpPath == "" {
		writeErr(w, http.StatusBadRequest, "version_and_file_required")
		return
	}

	// Аудит ДО подмены файла: без строки журнала загрузка не засчитывается.
	details := map[string]any{"version": version, "sha256": sum, "bytes": size, "name": vpxBinName}
	if err := s.st.scoped(r.Context(), tenantID, func(ctx context.Context, _ storage.TenantScope) error {
		return s.cfg.DB.WriteAuditLog(ctx, userID, email, "agent_build_uploaded", "agent_build", "windows_amd64_vpx", details)
	}); err != nil {
		s.internal(w, "audit agent build", err)
		return
	}

	final := filepath.Join(dir, vpxBinName)
	if err := os.Rename(tmpPath, final); err != nil {
		s.internal(w, "install build", err)
		return
	}
	tmpPath = ""
	_ = os.Chmod(final, 0o644)
	if err := writeAtomic(final+".sha256", fmt.Sprintf("%s  %s\n", sum, vpxBinName)); err != nil {
		s.internal(w, "write sha256", err)
		return
	}
	if err := writeAtomic(final+".version", version); err != nil {
		s.internal(w, "write version", err)
		return
	}
	s.log.Info("screen: vpx agent build uploaded", "version", version, "sha256", sum, "bytes", size, "by", email)
	now := time.Now()
	writeJSON(w, http.StatusCreated, vpxBuildInfo{Present: true, Version: version, SHA256: sum, Bytes: size, Uploaded: &now})
}

func writeAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
