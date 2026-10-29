//go:build linux

package security

// listProcesses перечисляет процессы по /proc — см. procfsProcesses о том,
// почему не ps (comm усекается ядром до 15 символов).
func listProcesses() ([]Process, error) {
	return procfsProcesses("/proc")
}
