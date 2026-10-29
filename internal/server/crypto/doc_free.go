//go:build !enterprise

// Package crypto (FileVault escrow age-seal) — enterprise-фича. В open-core-сборке
// пакет пуст (все файлы под //go:build enterprise): age не в графе зависимостей.
// Файл-заглушка держит пакет непустым, чтобы `go build ./...` не падал в open-core.
package crypto
