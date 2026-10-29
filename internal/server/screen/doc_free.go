//go:build !screen

// Package screen (серверная часть удалённого рабочего стола). Собирается только с тегом
// screen; без него пакет пуст, gateway.ScreenService не регистрируется, и ScreenSession
// отвечает Unimplemented. Заглушка держит пакет непустым для `go build ./...`.
package screen
