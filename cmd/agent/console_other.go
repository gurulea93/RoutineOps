//go:build !windows

package main

// attachParentConsole — пустышка вне Windows: подсистема GUI/CUI и проблема
// консольного окна специфичны для Windows (см. console_windows.go).
func attachParentConsole() {}
