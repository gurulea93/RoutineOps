//go:build darwin && cgo

// Полноэкранный замок блокировки для macOS на Cocoa (CGO). Аналог Windows-оверлея:
// окно поверх всех с полем пароля, оффлайн-разблок по bcrypt-хешу. Требует
// CGO-сборки (make build-mac-native); в CGO=0-сборке используется заглушка.
package lockui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework IOKit
#include <stdlib.h>
void lockui_show(const char* reason);
*/
import "C"

import (
	"log/slog"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/crypto/bcrypt"

	"github.com/Floodww/RoutineOps/internal/agent/lock"
)

// Состояние для колбэка из Cocoa (один замок за раз).
var (
	mu      sync.Mutex
	curHash string
	curPath string
	curLog  *slog.Logger
)

//export lockuiVerify
func lockuiVerify(cpw *C.char) C.int {
	pw := C.GoString(cpw)
	mu.Lock()
	hash, path, lg := curHash, curPath, curLog
	mu.Unlock()

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) != nil {
		return 0 // неверный пароль
	}
	// lock.json создан демоном под root — этот процесс (юзер-сессия) не может его
	// перезаписать напрямую (sticky-бит общего каталога блокирует rename чужого
	// файла, полевой баг v1.5.3: ClearState тихо падал permission denied). Демон
	// сверит пароль сам (Manager.processUnlockRequests) и снимет блокировку —
	// у него есть на это права, как владельца файла.
	if err := lock.WriteUnlockRequest(filepath.Dir(path), pw); err != nil && lg != nil {
		lg.Error("lock-screen: не удалось отправить запрос на разблокировку", slog.Any("error", err))
	}
	return 1
}

// Run показывает Cocoa-замок, если устройство заблокировано; блокирует поток до
// верного пароля (оффлайн-сверка с хешем из state-файла).
func Run(statePath string, log *slog.Logger) {
	st, err := lock.ReadState(statePath)
	if err != nil || !st.Locked {
		return
	}
	reason := st.Reason
	if reason == "" {
		reason = "Устройство заблокировано администратором. Обратитесь в IT для разблокировки."
	}

	mu.Lock()
	curHash, curPath, curLog = st.Hash, statePath, log
	mu.Unlock()

	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))
	C.lockui_show(cReason)
}
