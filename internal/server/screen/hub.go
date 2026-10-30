//go:build screen

package screen

import (
	"context"
	"sync"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Хаб сеанса RSF1: стрим агента пишет части кадров, зрители забирают их long-poll'ом
// GET /screen-sessions/{id}/live?since=N&wait=ms.
//
// Курсор — номер записи, а не seq кадра: кадр режется на части (Split), и зритель,
// оборвавшийся посреди кадра, обязан продолжить с той же части. Буфер начинается с
// ключевого кадра: зритель с since=0 или отставший (since < base) получает всё с него и
// рисует полный экран, а не дельты к холсту, которого у него нет.

const (
	// hubMaxBuffer — потолок буфера между ключевыми кадрами. Переполнение — сброс и
	// запрос ключевого кадра: держать неограниченную историю ради одного медленного
	// зрителя значит дать ему съесть память сервера.
	hubMaxBuffer = 32 << 20
	// hubMaxResponse — потолок тела одного ответа /live.
	hubMaxResponse = 8 << 20
	// MaxWait — потолок long-poll (панель держит 20 с, ниже серверных таймаутов записи).
	MaxWait = 25 * time.Second
	// keyframeOnJoinAfter — новому зрителю просим свежий ключевой кадр, если буферный
	// старше: иначе он проигрывает всю накопленную цепочку дельт.
	keyframeOnJoinAfter = 5 * time.Second
)

type hub struct {
	mu        sync.Mutex
	base      uint64 // номер первой записи в recs
	recs      [][]byte
	size      int
	valid     bool   // буфер начинается с ключевого кадра
	kfSeq     uint32 // seq ключевого кадра, с которого начат буфер
	kfAt      time.Time
	w, h      uint16
	closed    bool
	endReason string
	wake      chan struct{} // закрывается и заменяется на каждое изменение
	lastPoll  time.Time
	paused    bool // агент приостановил выдачу кадров (FrameFlow)
	pauseWhy  string

	keyframe chan struct{} // → стрим: попросить агента о ключевом кадре
	stop     chan string   // → стрим: закрыть сеанс с причиной

	// Управление (Ф3). input и narrow читает только стрим сеанса: он единственный, кто
	// пишет в stream.Send, и потому единственный, кто может отправить агенту ввод.
	input  chan *pb.SessionInput
	narrow chan struct{} // → стрим: вернуть управление сотруднику (сужение области)

	// Кому принадлежит управление. Заполняется при открытии стрима из строки сеанса, чтобы
	// ручка ввода (десятки запросов в секунду) не ходила за ними в БД.
	tenantID, operatorID string
	control, returned    bool

	// Мониторы: сколько их у машины и какой показан (по заголовкам кадров), а также какой
	// хочет видеть оператор. Выбор уходит агенту сужением области — см. narrowScope.
	displays, display, wantDisp int
	counts                      inputCounts
}

func newHub(w, h uint16) *hub {
	return &hub{
		w: w, h: h,
		wake:     make(chan struct{}),
		lastPoll: time.Now(),
		keyframe: make(chan struct{}, 1),
		stop:     make(chan string, 1),
		input:    make(chan *pb.SessionInput, inputQueue),
		narrow:   make(chan struct{}, 1),
	}
}

func (b *hub) broadcastLocked() {
	close(b.wake)
	b.wake = make(chan struct{})
}

func (b *hub) requestKeyframe() {
	select {
	case b.keyframe <- struct{}{}:
	default:
	}
}

// Stop просит стрим закрыть сеанс. Первая причина побеждает.
func (b *hub) Stop(reason string) {
	select {
	case b.stop <- reason:
	default:
	}
}

// append кладёт часть кадра. rec — уже собранная запись [len][rel_ms][payload].
func (b *hub) append(p *screenframe.Part, rec []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if p.Keyframe && (!b.valid || p.Seq != b.kfSeq) {
		// Новый ключевой кадр: всё до него зрителям больше не нужно.
		b.base += uint64(len(b.recs))
		b.recs, b.size = nil, 0
		b.valid, b.kfSeq, b.kfAt = true, p.Seq, time.Now()
	}
	if p.Width != 0 && p.Height != 0 {
		b.w, b.h = p.Width, p.Height
	}
	if p.Displays != 0 {
		b.displays, b.display = int(p.Displays), int(p.Display)
	}
	if !b.valid {
		// Дельты без ключевого кадра рисовать не на чем; ждём его.
		b.base++
		return
	}
	if b.size+len(rec) > hubMaxBuffer {
		b.base += uint64(len(b.recs)) + 1
		b.recs, b.size, b.valid = nil, 0, false
		b.requestKeyframe()
		b.broadcastLocked()
		return
	}
	b.recs = append(b.recs, rec)
	b.size += len(rec)
	b.broadcastLocked()
}

func (b *hub) close(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed, b.endReason = true, reason
	b.broadcastLocked()
}

// liveResult — ответ одному опросу.
type liveResult struct {
	Body      []byte
	Next      uint64
	W, H      uint16
	Closed    bool
	EndReason string
	Paused    bool
	PauseWhy  string
}

// poll отдаёт записи с номера since, ожидая до wait, если новых нет.
func (b *hub) poll(ctx context.Context, since uint64, wait time.Duration) liveResult {
	if wait > MaxWait {
		wait = MaxWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	b.mu.Lock()
	b.lastPoll = time.Now()
	if since == 0 && time.Since(b.kfAt) > keyframeOnJoinAfter {
		b.requestKeyframe()
	}
	paused0 := b.paused
	b.mu.Unlock()

	for {
		b.mu.Lock()
		end := b.base + uint64(len(b.recs))
		if since < b.base || since > end {
			// Отстал или пришёл с чужим курсором — с начала буфера (ключевой кадр).
			since = b.base
		}
		if since < end || b.closed || b.paused != paused0 {
			res := b.resultLocked(since)
			b.mu.Unlock()
			return res
		}
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-wake:
		case <-timer.C:
			b.mu.Lock()
			res := b.resultLocked(since)
			b.mu.Unlock()
			return res
		case <-ctx.Done():
			return liveResult{Next: since}
		}
	}
}

// resultLocked собирает ответ с записи since (since уже в [base, end]).
func (b *hub) resultLocked(since uint64) liveResult {
	res := liveResult{W: b.w, H: b.h, Closed: b.closed, EndReason: b.endReason, Paused: b.paused, PauseWhy: b.pauseWhy}
	end := b.base + uint64(len(b.recs))
	i := since - b.base
	for ; i < uint64(len(b.recs)); i++ {
		r := b.recs[i]
		if len(res.Body) > 0 && len(res.Body)+len(r) > hubMaxResponse {
			break
		}
		res.Body = append(res.Body, r...)
	}
	res.Next = b.base + i
	// Закрытие сообщаем, только когда зритель дочитал всё: последние кадры важнее.
	if res.Next < end {
		res.Closed, res.EndReason = false, ""
	}
	return res
}

// idleFor — сколько нет ни одного опроса зрителя (§9.9 viewer liveness).
func (b *hub) idleFor() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Since(b.lastPoll)
}

// hubs — живые хабы процесса по id сеанса.
type hubs struct {
	mu sync.Mutex
	m  map[string]*hub
}

func (hs *hubs) get(id string) *hub {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.m[id]
}

// put регистрирует хаб; false — у сеанса уже есть живой стрим (второй hello).
func (hs *hubs) put(id string, b *hub) bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.m == nil {
		hs.m = map[string]*hub{}
	}
	if _, ok := hs.m[id]; ok {
		return false
	}
	hs.m[id] = b
	return true
}

func (hs *hubs) remove(id string, b *hub) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.m[id] == b {
		delete(hs.m, id)
	}
}

// setFlow — агент сообщил о паузе/возобновлении кадров (заголовок X-Screen-Frames).
func (b *hub) setFlow(paused bool, why string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused, b.pauseWhy = paused, why
	b.broadcastLocked()
}
