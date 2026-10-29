//go:build screen

package screen

import (
	"io"
	"sync"

	"google.golang.org/grpc"

	pb "github.com/Floodww/RoutineOps/proto"
)

// fakeScreenStream — стрим сеанса для тестов без gRPC. grpc.ClientStream встроен nil:
// его методы тестам не нужны, и вызов любого из них — паника, то есть ошибка теста.
type fakeScreenStream struct {
	grpc.ClientStream

	mu      sync.Mutex
	sent    []*pb.SessionUp
	failAt  int // номер Send (с 1), который вернёт failErr; 0 — не падать
	failErr error
	calls   int

	recvQueue []*pb.SessionDown // что вернёт Recv по порядку; заполняется push
	recvErr   error             // после опустевшей очереди; nil → io.EOF
}

// push кладёт сообщение сервера в очередь Recv.
func (f *fakeScreenStream) push(m *pb.SessionDown) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recvQueue = append(f.recvQueue, m)
}

var _ pb.AgentService_ScreenSessionClient = (*fakeScreenStream)(nil)

// Send записывает сообщение; на failAt-м вызове возвращает failErr и ничего не пишет.
func (f *fakeScreenStream) Send(m *pb.SessionUp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		return f.failErr
	}
	f.sent = append(f.sent, m)
	return nil
}

// Recv отдаёт очередь push по порядку, затем recvErr (или io.EOF, если он не задан).
// Пустая очередь без push — io.EOF сразу, как раньше.
func (f *fakeScreenStream) Recv() (*pb.SessionDown, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recvQueue) > 0 {
		m := f.recvQueue[0]
		f.recvQueue = f.recvQueue[1:]
		return m, nil
	}
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	return nil, io.EOF
}

// messages — копия отправленного на этот момент.
func (f *fakeScreenStream) messages() []*pb.SessionUp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.SessionUp(nil), f.sent...)
}
