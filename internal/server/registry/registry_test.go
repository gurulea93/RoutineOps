package registry

import (
	"sync"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Register должен вернуть рабочий канал, а Send — доставить в него задачу.
func TestRegisterAndSend_Delivers(t *testing.T) {
	r := New()
	ch, cancel := r.Register("dev-1")
	defer cancel()

	if !r.Connected("dev-1") {
		t.Fatal("Connected = false сразу после Register, ожидалось true")
	}

	task := &pb.Task{TaskId: "t-1"}
	if !r.Send("dev-1", task) {
		t.Fatal("Send вернул false для подключённого устройства")
	}

	got := <-ch
	if got.TaskId != "t-1" {
		t.Errorf("получен TaskId %q, ожидался %q", got.TaskId, "t-1")
	}
}

// Send неизвестному устройству должен вернуть false, а не паниковать.
func TestSend_Unconnected_ReturnsFalse(t *testing.T) {
	r := New()
	if r.Send("ghost", &pb.Task{TaskId: "t"}) {
		t.Error("Send вернул true для неподключённого устройства")
	}
	if r.Connected("ghost") {
		t.Error("Connected = true для незарегистрированного устройства")
	}
}

// Когда буфер канала (16) заполнен, Send должен вернуть false без блокировки.
func TestSend_FullBuffer_ReturnsFalse(t *testing.T) {
	r := New()
	_, cancel := r.Register("dev-1")
	defer cancel()

	// Заполняем весь буфер (16) — никто не читает.
	for i := 0; i < 16; i++ {
		if !r.Send("dev-1", &pb.Task{TaskId: "fill"}) {
			t.Fatalf("Send #%d вернул false до заполнения буфера", i)
		}
	}
	// 17-я задача не помещается — должно быть false, без блокировки.
	if r.Send("dev-1", &pb.Task{TaskId: "overflow"}) {
		t.Error("Send вернул true при переполненном буфере")
	}
}

// cancel должен снять регистрацию, закрыть канал и сделать последующий Send безопасным.
func TestCancel_RemovesAndClosesChannel(t *testing.T) {
	r := New()
	ch, cancel := r.Register("dev-1")
	cancel()

	if r.Connected("dev-1") {
		t.Error("Connected = true после cancel")
	}
	if r.Send("dev-1", &pb.Task{TaskId: "t"}) {
		t.Error("Send вернул true после cancel")
	}
	// Канал закрыт: чтение должно немедленно вернуть zero value и ok=false.
	if _, ok := <-ch; ok {
		t.Error("канал не закрыт после cancel")
	}
}

// Reconnect: повторный Register заменяет канал, а старый cancel НЕ должен
// удалять новую регистрацию (иначе устройство теряет задачи после reconnect).
func TestReRegister_OldCancelKeepsNewRegistration(t *testing.T) {
	r := New()
	_, cancel1 := r.Register("dev-1")
	ch2, cancel2 := r.Register("dev-1")
	defer cancel2()

	// Старый defer cancel срабатывает уже ПОСЛЕ reconnect.
	cancel1()

	// Новая регистрация должна остаться живой.
	if !r.Connected("dev-1") {
		t.Fatal("устройство отвалилось после cancel старого соединения (reconnect-баг)")
	}
	if !r.Send("dev-1", &pb.Task{TaskId: "after-reconnect"}) {
		t.Fatal("Send в новый канал не прошёл после старого cancel")
	}
	got := <-ch2
	if got.TaskId != "after-reconnect" {
		t.Errorf("получен TaskId %q, ожидался %q", got.TaskId, "after-reconnect")
	}
}

// Гонки: параллельные Register/Send/Connected/cancel не должны вызывать
// data race (запускать с -race) или панику на закрытом канале.
func TestConcurrentAccess_NoRace(t *testing.T) {
	r := New()
	const workers = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			dev := "dev"
			ch, cancel := r.Register(dev)
			// Дренируем канал, чтобы Send-ы кого-то из горутин проходили.
			go func() {
				for range ch {
				}
			}()
			r.Connected(dev)
			r.Send(dev, &pb.Task{TaskId: "t"})
			cancel()
		}(i)
	}
	wg.Wait()
}
