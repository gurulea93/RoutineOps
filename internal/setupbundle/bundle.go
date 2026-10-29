// Package setupbundle — формат установщика RoutineOps-setup.exe: готовый к запуску exe,
// к хвосту которого приклеены MSI агента и параметры энроллмента.
//
// Зачем. Универсальный MSI не знает ни сервера, ни токена устройства, и при двойном клике
// требует ввести пять свойств. Панель знает их все: при создании устройства она получает
// одноразовый токен и пин CA. Поэтому панель собирает установщик прямо в браузере (stub
// + MSI + параметры), а человек запускает один файл. Сервер в сборке не участвует и токена
// не хранит.
//
// Раскладка файла:
//
//	[stub.exe][msi][json параметров][u32 LE: длина msi][u32 LE: длина json]["ROPSET01"]
//
// Читать хвост с конца — единственный способ, не парся PE: заголовок exe не знает про
// приклеенные данные, а Windows их игнорирует (overlay).
package setupbundle

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	magic      = "ROPSET01"
	trailerLen = 4 + 4 + len(magic)
	// MaxMSI и maxJSON — потолки: чужой хвост не должен заставить выделить гигабайты.
	MaxMSI  = 256 << 20
	maxJSON = 16 << 10
)

// Params — свойства MSI (build/msi/mdm-agent.wxs).
type Params struct {
	EnrollURL   string `json:"enroll_url"`
	EnrollToken string `json:"enroll_token"`
	CAURL       string `json:"ca_url"`
	CASHA256    string `json:"ca_sha256"`
	ServerAddr  string `json:"server_addr"`
}

var errBundle = errors.New("setupbundle: no valid payload in this file")

// Validate: все пять свойств нужны (launch condition MSI иначе прервёт установку), и в
// значениях нет символов, которые сломали бы командную строку msiexec.
func (p Params) Validate() error {
	for name, v := range map[string]string{
		"enroll_url": p.EnrollURL, "enroll_token": p.EnrollToken, "ca_url": p.CAURL,
		"ca_sha256": p.CASHA256, "server_addr": p.ServerAddr,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("setupbundle: %s is empty", name)
		}
		if strings.ContainsAny(v, "\"\r\n\x00") {
			return fmt.Errorf("setupbundle: %s contains a forbidden character", name)
		}
	}
	return nil
}

// MSIArgs — командная строка msiexec (без имени программы).
func (p Params) MSIArgs(msiPath, logPath string) string {
	return fmt.Sprintf(`/i "%s" /qb /norestart /l*v "%s" ENROLL_URL="%s" ENROLL_TOKEN="%s" CA_URL="%s" CA_SHA256="%s" SERVER_ADDR="%s"`,
		msiPath, logPath, p.EnrollURL, p.EnrollToken, p.CAURL, p.CASHA256, p.ServerAddr)
}

// Append дописывает хвост (MSI, параметры, трейлер) после тела stub. Ту же раскладку
// строит панель в браузере (web/src/lib/setupBundle.ts); тест держит их согласованными.
func Append(w io.Writer, msi []byte, p Params) error {
	if err := p.Validate(); err != nil {
		return err
	}
	js, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var tr [trailerLen]byte
	binary.LittleEndian.PutUint32(tr[0:], uint32(len(msi)))
	binary.LittleEndian.PutUint32(tr[4:], uint32(len(js)))
	copy(tr[8:], magic)
	for _, b := range [][]byte{msi, js, tr[:]} {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// Read достаёт MSI и параметры из файла установщика.
func Read(r io.ReaderAt, size int64) (Params, []byte, error) {
	var p Params
	if size < int64(trailerLen) {
		return p, nil, errBundle
	}
	var tr [trailerLen]byte
	if _, err := r.ReadAt(tr[:], size-int64(trailerLen)); err != nil {
		return p, nil, err
	}
	if string(tr[8:]) != magic {
		return p, nil, errBundle
	}
	msiLen := int64(binary.LittleEndian.Uint32(tr[0:]))
	jsLen := int64(binary.LittleEndian.Uint32(tr[4:]))
	if msiLen == 0 || msiLen > MaxMSI || jsLen == 0 || jsLen > maxJSON ||
		msiLen+jsLen+int64(trailerLen) > size {
		return p, nil, errBundle
	}
	start := size - int64(trailerLen) - jsLen - msiLen
	msi := make([]byte, msiLen)
	if _, err := r.ReadAt(msi, start); err != nil {
		return p, nil, err
	}
	js := make([]byte, jsLen)
	if _, err := r.ReadAt(js, start+msiLen); err != nil {
		return p, nil, err
	}
	if err := json.Unmarshal(js, &p); err != nil {
		return Params{}, nil, errBundle
	}
	if err := p.Validate(); err != nil {
		return Params{}, nil, err
	}
	return p, msi, nil
}
