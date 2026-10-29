//go:build !enterprise

// Package filevault (FileVault dynamic-lock + escrow) — enterprise-фича. В open-core
// пакет пуст (все файлы под //go:build enterprise). Заглушка держит пакет непустым
// для `go build ./...`.
package filevault
