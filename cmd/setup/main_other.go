//go:build !windows

// Установщик есть только для Windows; файл нужен, чтобы пакет собирался (go vet ./...) на
// остальных ОС.
package main

func main() {}
