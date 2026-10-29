//go:build !windows

package lock

import "os"

// EnsureUserWritableDir создаёт dir и делает его доступным на запись всем локальным
// пользователям (0o1777, как /tmp — sticky-бит не даёт удалить/переименовать чужие
// файлы, только создавать свои). Каталог создаёт служба под root, но писать в него
// должна и юзер-сессия без прав root: лок-экран снимает блокировку (ClearState),
// трей кладёт заявку на админ-права. Без этого ClearState тихо падает с "permission
// denied" (0o755 = запись только root), лок-экран считает пароль принятым (закрывает
// окно), а файл состояния остаётся locked:true — service.SessionLocker поднимает
// оверлей заново на ближайшем tick (полевой баг v1.5.2: разблок паролем не держался,
// пока устройство онлайн). Зеркало Windows-варианта (acl_windows.go), где то же самое
// делает DACL. MkdirAll режет режим umask-ом процесса — Chmod довозводит его явно.
func EnsureUserWritableDir(dir string) error {
	// os.FileMode хранит sticky/setuid/setgid отдельными старшими битами, а не как
	// биты 09 традиционного mode_t — литерал 0o1777 их НЕ выставляет (Chmod получил
	// бы обычные 0o777 без sticky). Бит нужно задавать через os.ModeSticky явно.
	const mode = os.ModeSticky | 0o777
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}
