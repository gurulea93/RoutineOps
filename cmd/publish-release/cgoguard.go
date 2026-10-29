package main

import (
	"debug/buildinfo"
	"errors"
	"fmt"
)

// errNotCGO — бинарь собран без cgo. Отдельный сентинел, чтобы тест не сверялся с текстом.
var errNotCGO = errors.New("бинарь собран с CGO_ENABLED=0")

// requireCGODarwin отклоняет публикацию darwin-бинаря, собранного без cgo.
//
// Зачем: cgo на macOS тянет Cocoa (полноэкранный замок блокировки) и Security.framework
// (Keychain). При CGO_ENABLED=0 сборка НЕ падает — build-теги `!darwin || !cgo` молча
// подставляют заглушки (lockui_other.go, provider_stub.go), и агент уезжает в парк без
// замка и без keychain. Такой бинарь уже однажды публиковали.
//
// sha256/.version это не ловят: они защищают от порчи файла, а не от «не той сборки» —
// подменённый бинарь с пересчитанным хешем проходил цепочку молча. Проверка стоит здесь,
// в publish-release, потому что это единственная воронка, через которую бинарь попадает
// в agent_releases (и install.sh, и update.sh зовут только его).
//
// buildinfo.ReadFile читает Go-buildinfo кросс-платформенно (в alpine-контейнере деплоя
// и в linux-CI тоже) и переживает -ldflags "-s -w"; otool сработал бы только на маке.
func requireCGODarwin(path string) error {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("чтение buildinfo из %s: %w", path, err)
	}
	for _, s := range bi.Settings {
		if s.Key == "CGO_ENABLED" {
			if s.Value == "1" {
				return nil
			}
			return fmt.Errorf("%w: пересоберите на маке через `make build-mac-native` "+
				"(CGO_ENABLED=0 GOOS=darwin собирает агента без Cocoa-замка и Keychain)", errNotCGO)
		}
	}
	return fmt.Errorf("в buildinfo %s нет ключа CGO_ENABLED — бинарь собран не тем тулчейном", path)
}
