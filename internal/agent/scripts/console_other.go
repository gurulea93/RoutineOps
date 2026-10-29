//go:build !darwin && !windows && !linux

package scripts

// osConsoleUser — заглушка для неподдерживаемых ОС (login/logout-события не детектятся).
func osConsoleUser() string { return "" }
