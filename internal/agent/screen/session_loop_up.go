//go:build screen

package screen

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Петля «вверх»: кадры захватчика → SessionFrame на стриме сеанса. Пока — только
// сигнатуры; тела заполняются по шагам.

// upFrame — собранный кадр: все его части по порядку.
type upFrame struct {
	seq   uint32
	key   bool
	parts []wireFrame
}

// errBadFrame — кадр захватчика не прошёл проверку и не уходит на сервер вовсе. Это
// ошибка, а не dropped: выброс по «последний побеждает» — штатная работа, битый кадр —
// нет, и смешивать их в одном счётчике значит прятать поломку за перегрузкой.
var errBadFrame = errors.New("screen: malformed frame from worker")

// checkParts проверяет кадр, уже нарезанный захватчиком (screenframe.Split), целиком:
// принимается только весь кадр. Нарезать заново здесь нельзя — сервер разбирает каждую
// часть как самостоятельную RSF1-часть, и срез по границе байтов он отвергнет.
func checkParts(parts []wireFrame) error {
	if len(parts) == 0 {
		return fmt.Errorf("%w: no parts", errBadFrame)
	}
	seq, key := parts[0].Seq, parts[0].Keyframe
	for i, p := range parts {
		if len(p.Payload) > screenframe.MaxPartBytes {
			return fmt.Errorf("%w: part %d is %d B", errBadFrame, i, len(p.Payload))
		}
		if p.Last != (i == len(parts)-1) {
			return fmt.Errorf("%w: part %d last_part=%v of %d parts", errBadFrame, i, p.Last, len(parts))
		}
		if p.Seq != seq || p.Keyframe != key {
			return fmt.Errorf("%w: part %d seq/keyframe %d/%v, frame %d/%v", errBadFrame, i, p.Seq, p.Keyframe, seq, key)
		}
		h, err := screenframe.ParsePart(p.Payload)
		if err != nil {
			return fmt.Errorf("%w: part %d: %v", errBadFrame, i, err)
		}
		if h.Seq != p.Seq || h.Keyframe != p.Keyframe || h.Last != p.Last {
			return fmt.Errorf("%w: part %d header disagrees with framing", errBadFrame, i)
		}
	}
	return nil
}

// bandwidthBudget — бюджет полосы профиля, ведро токенов ёмкостью в одну секунду бюджета:
// после простоя ведро наполняется, но не больше, чем на секунду, — накопленной за минуту
// тишины «рафалы» не бывает. Сам он не спит: wait только считает паузу, а выдерживает её
// петля, и пауза никогда не длиннее MaxPause.
type bandwidthBudget struct {
	rate     float64 // байт/с
	burst    float64 // ёмкость ведра, байт
	maxPause time.Duration
	tokens   float64 // может уходить в минус: долг, который гасит пауза
	last     time.Time
	now      func() time.Time
	stalls   uint32
}

// newBandwidthBudget — бюджет из профиля. Профиль без бюджета (medium в Ф1) получает
// бюджет low: это осторожная сторона, а не выдумка нового числа.
func newBandwidthBudget(p Profile, now func() time.Time) *bandwidthBudget {
	if p.MaxBitsPerSec <= 0 || p.MaxPause <= 0 {
		p.MaxBitsPerSec, p.MaxPause = ProfileLow.MaxBitsPerSec, ProfileLow.MaxPause
	}
	if now == nil {
		now = time.Now
	}
	rate := float64(p.MaxBitsPerSec) / 8
	return &bandwidthBudget{rate: rate, burst: rate, maxPause: p.MaxPause, tokens: rate, last: now(), now: now}
}

// setProfile меняет бюджет на бюджет профиля (смена качества посреди сеанса). Накопленные
// токены срезаются до нового потолка.
func (b *bandwidthBudget) setProfile(p Profile) {
	if p.MaxBitsPerSec <= 0 || p.MaxPause <= 0 {
		p.MaxBitsPerSec, p.MaxPause = ProfileLow.MaxBitsPerSec, ProfileLow.MaxPause
	}
	b.rate = float64(p.MaxBitsPerSec) / 8
	b.burst, b.maxPause = b.rate, p.MaxPause
	b.tokens = min(b.tokens, b.burst)
}

// wait списывает n байт и возвращает паузу перед их отправкой. capped — нужная пауза
// длиннее MaxPause: пауза срезается до потолка, долг сверх потолка прощается (иначе он
// копился бы и держал петлю всё дольше), и считается stall. n ≤ 0 состояние не трогает.
func (b *bandwidthBudget) wait(n int) (pause time.Duration, capped bool) {
	if n <= 0 {
		return 0, false
	}
	if t := b.now(); t.After(b.last) {
		b.tokens = min(b.burst, b.tokens+b.rate*t.Sub(b.last).Seconds())
		b.last = t
	}
	b.tokens -= float64(n)
	if b.tokens >= 0 {
		return 0, false
	}
	pause = time.Duration(-b.tokens / b.rate * float64(time.Second))
	if pause > b.maxPause {
		pause, capped = b.maxPause, true
		b.tokens = -b.rate * b.maxPause.Seconds()
		b.stalls++
	}
	return pause, capped
}

// Stalls — сколько раз пауза упёрлась в потолок (SessionStats.stalls).
func (b *bandwidthBudget) Stalls() uint32 { return b.stalls }

// latestFrame — «последний кадр побеждает» на целых кадрах. Ждёт отправки не больше
// одного кадра: новый складывается с ожидающим (compose), и сумма не больше полного
// кадра — тайлы одной сетки. Если сложить нельзя, ожидающий выбрасывается, захватчику
// уходит запрос ключевого кадра, и до него выбрасываются и дельты: они посчитаны от
// кадра, которого зритель уже не увидит.
type latestFrame struct {
	mu      sync.Mutex
	f       *upFrame
	needKey bool
	dropped uint32 // кадры, не ушедшие отдельно (сложены или выброшены)
	errors  uint32 // кадры, не прошедшие checkParts после сложения
	askKey  func() // msgKeyframe захватчику; зовётся вне мьютекса
	// ready — «есть что отправить», ёмкость 1. Лишний сигнал безвреден: take вернёт nil.
	ready chan struct{}
}

func newLatestFrame(askKey func()) *latestFrame {
	return &latestFrame{askKey: askKey, ready: make(chan struct{}, 1)}
}

// Ready — сигнал для петли отправки: после него стоит позвать take.
func (l *latestFrame) Ready() <-chan struct{} { return l.ready }

// put кладёт проверенный кадр; true — ожидающий кадр не уйдёт отдельно (сложен или
// выброшен) либо выброшен сам новый. Не блокируется никогда: сигнал ready неблокирующий.
func (l *latestFrame) put(f *upFrame) bool {
	ask, dropped := l.putLocked(f)
	if ask && l.askKey != nil {
		l.askKey()
	}
	l.mu.Lock()
	pending := l.f != nil
	l.mu.Unlock()
	if pending {
		select {
		case l.ready <- struct{}{}:
		default:
		}
	}
	return dropped
}

func (l *latestFrame) putLocked(f *upFrame) (ask, dropped bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.needKey && !f.key {
		l.dropped++
		return false, true
	}
	if f.key {
		l.needKey = false
	}
	if l.f == nil {
		l.f = f
		return false, false
	}
	merged, err := compose(l.f, f)
	bad := err == nil && checkParts(merged.parts) != nil
	if err == nil && !bad {
		l.f = merged
		l.dropped++
		return false, true
	}
	l.f = nil
	if bad {
		// Битая сумма — одна ошибка, а не выбросы. Ключевой новый сам прошёл checkParts
		// до put и от старого кадра не зависит: остаётся, запрашивать нечего. Дельта без
		// старого кадра бесполезна: уходит вместе с ним, до ключевого кадра.
		l.errors++
		if f.key {
			l.f = f
			return false, true
		}
		l.needKey = true
		return true, true
	}
	// Сложить нельзя: ожидающий выброшен. Ключевой новый самодостаточен (checkParts он
	// прошёл до put); дельта — нет.
	l.dropped++
	if f.key {
		l.f = f
		return false, true
	}
	l.dropped++
	l.needKey = true
	return true, true
}

// take забирает кадр к отправке; nil — отправлять нечего.
func (l *latestFrame) take() *upFrame {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.f
	l.f = nil
	return f
}

// counters — (dropped, errors) для SessionStats и лога.
func (l *latestFrame) counters() (dropped, errs uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped, l.errors
}

// loopUp ведёт петлю до конца сеанса захватчика или обрыва стрима.
func (s *session) loopUp(src io.Reader, st *sessionStream) (EndReason, error) {
	return "", nil
}
