package registry

import (
	"sync"

	pb "github.com/Floodww/RoutineOps/proto"
)

type Registry struct {
	mu    sync.Mutex
	conns map[string]chan<- *pb.Task
}

func New() *Registry {
	return &Registry{conns: make(map[string]chan<- *pb.Task)}
}

// Register регистрирует подключённое устройство. Возвращает канал задач
// и функцию отмены регистрации - вызвать через defer при disconnect.
func (r *Registry) Register(deviceID string) (<-chan *pb.Task, func()) {
	ch := make(chan *pb.Task, 16)
	r.mu.Lock()
	r.conns[deviceID] = ch
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		// Удаляем запись только если в мапе всё ещё наш канал. Иначе при
		// быстром reconnect старый cancel снёс бы уже новую регистрацию,
		// и устройство перестало бы получать задачи.
		if cur, ok := r.conns[deviceID]; ok && cur == ch {
			delete(r.conns, deviceID)
		}
		r.mu.Unlock()
		close(ch)
	}
}

// Send отправляет задачу подключённому устройству.
// Возвращает false если устройство не подключено или буфер полон.
//
// Неблокирующая отправка выполняется ПОД тем же mu, что и delete в cancel: канал
// закрывается (cancel) только после удаления из conns, поэтому удерживаемый здесь
// под локом канал гарантированно ещё открыт — иначе возможна гонка "send on
// closed channel" при отключении устройства одновременно с отправкой задачи.
func (r *Registry) Send(deviceID string, task *pb.Task) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.conns[deviceID]
	if !ok {
		return false
	}
	select {
	case ch <- task:
		return true
	default:
		return false
	}
}

// Connected возвращает true если устройство сейчас подключено.
func (r *Registry) Connected(deviceID string) bool {
	r.mu.Lock()
	_, ok := r.conns[deviceID]
	r.mu.Unlock()
	return ok
}
