//go:build screen

package screen

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Сетка 300×200 при TileSize=128: x ∈ {0,128,256}, y ∈ {0,128}, краевые тайлы неполные.
var gridAll = []tilePos{{0, 0}, {128, 0}, {256, 0}, {0, 128}, {128, 128}, {256, 128}}

// gridFrame собирает кадр w×h с тайлами в указанных позициях; JPEG тайла — один байт-метка,
// по которому видно, из какого кадра тайл.
func gridFrame(t *testing.T, seq uint32, key bool, w, h int, mark byte, pos ...tilePos) *upFrame {
	t.Helper()
	var tiles []screenframe.Tile
	for _, p := range pos {
		tiles = append(tiles, screenframe.Tile{X: p.x, Y: p.y,
			W: uint16(min(TileSize, w-int(p.x))), H: uint16(min(TileSize, h-int(p.y))), JPEG: []byte{mark}})
	}
	raw, err := screenframe.Split(seq, key, uint16(w), uint16(h), tiles, screenframe.MaxPartBytes)
	if err != nil {
		t.Fatal(err)
	}
	f := &upFrame{seq: seq, key: key}
	for i, p := range raw {
		f.parts = append(f.parts, wireFrame{Seq: seq, Keyframe: key, Last: i == len(raw)-1, Payload: p})
	}
	return f
}

// marks — метка каждого тайла кадра; заодно кадр обязан проходить checkParts.
func marks(t *testing.T, f *upFrame) map[tilePos]byte {
	t.Helper()
	if err := checkParts(f.parts); err != nil {
		t.Fatalf("composed frame fails checkParts: %v", err)
	}
	_, _, tiles, err := frameTiles(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[tilePos]byte{}
	for p, tl := range tiles {
		out[p] = tl.JPEG[0]
	}
	return out
}

func TestLatestFrameNoDropWhenEmptyAndTakeEmpties(t *testing.T) {
	l := newLatestFrame(nil)
	f := gridFrame(t, 1, true, 300, 200, 1, gridAll...)
	if l.put(f) {
		t.Fatal("nothing pending, nothing may be dropped")
	}
	if l.take() != f || l.take() != nil {
		t.Fatal("take must return the pending frame once")
	}
	if d, e := l.counters(); d != 0 || e != 0 {
		t.Fatalf("counters %d %d", d, e)
	}
}

func TestComposeDeltaOverDelta(t *testing.T) {
	l := newLatestFrame(func() { t.Fatal("keyframe requested for composable frames") })
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}, tilePos{128, 0}))
	if !l.put(gridFrame(t, 2, false, 300, 200, 2, tilePos{0, 0}, tilePos{0, 128})) {
		t.Fatal("pending frame was composed away, put must report it")
	}
	got := l.take()
	m := marks(t, got)
	want := map[tilePos]byte{{0, 0}: 2, {128, 0}: 1, {0, 128}: 2}
	if len(m) != len(want) || got.seq != 2 || got.key {
		t.Fatalf("seq %d key %v tiles %v", got.seq, got.key, m)
	}
	for p, v := range want {
		if m[p] != v {
			t.Fatalf("tile %v = %d, want %d", p, m[p], v)
		}
	}
	if d, _ := l.counters(); d != 1 {
		t.Fatalf("dropped %d", d)
	}
}

func TestComposeKeyframeOverDelta(t *testing.T) {
	l := newLatestFrame(nil)
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}))
	l.put(gridFrame(t, 2, true, 300, 200, 2, gridAll...))
	got := l.take()
	m := marks(t, got)
	if !got.key || len(m) != len(gridAll) {
		t.Fatalf("key %v, %d tiles", got.key, len(m))
	}
	for p, v := range m {
		if v != 2 {
			t.Fatalf("tile %v from the old delta survived a newer keyframe", p)
		}
	}
}

func TestComposeDeltaOverPendingKeyframe(t *testing.T) {
	l := newLatestFrame(nil)
	l.put(gridFrame(t, 1, true, 300, 200, 1, gridAll...))
	l.put(gridFrame(t, 2, false, 300, 200, 2, tilePos{128, 128}))
	got := l.take()
	m := marks(t, got)
	if !got.key || got.seq != 2 || len(m) != len(gridAll) {
		t.Fatalf("pending keyframe lost: key %v seq %d tiles %d", got.key, got.seq, len(m))
	}
	for p, v := range m {
		if want := byte(1); p == (tilePos{128, 128}) && v != 2 || p != (tilePos{128, 128}) && v != want {
			t.Fatalf("tile %v = %d", p, v)
		}
	}
}

func TestGridMismatchRequestsKeyframe(t *testing.T) {
	var asked int
	l := newLatestFrame(func() { asked++ })
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}))
	if !l.put(gridFrame(t, 2, false, 640, 480, 2, tilePos{0, 0})) {
		t.Fatal("drop not reported")
	}
	if asked != 1 || l.take() != nil {
		t.Fatalf("asked %d, pending must be empty", asked)
	}
	// До ключевого кадра дельты выбрасываются.
	if !l.put(gridFrame(t, 3, false, 640, 480, 3, tilePos{0, 0})) || l.take() != nil {
		t.Fatal("delta accepted before keyframe")
	}
	key := gridFrame(t, 4, true, 640, 480, 4, tilePos{0, 0})
	if l.put(key) || l.take() != key {
		t.Fatal("keyframe after mismatch not accepted")
	}
	if d, e := l.counters(); d != 3 || e != 0 || asked != 1 {
		t.Fatalf("dropped %d errors %d asked %d", d, e, asked)
	}
}

func TestOffGridTileIsMismatch(t *testing.T) {
	var asked int
	l := newLatestFrame(func() { asked++ })
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}))
	bad := gridFrame(t, 2, false, 300, 200, 2, tilePos{64, 0}) // не на сетке
	l.put(bad)
	if asked != 1 || l.take() != nil {
		t.Fatalf("off-grid tile composed: asked %d", asked)
	}
}

// Конкурентный доступ: писатель и читатель одновременно. Здесь без -race (нет CGO),
// но тест рассчитан на запуск с -race там, где он доступен.
func TestLatestFrameConcurrent(t *testing.T) {
	var asked atomic.Int32
	l := newLatestFrame(func() { asked.Add(1) })
	frames := make([]*upFrame, 200)
	for i := range frames {
		pos := []tilePos{gridAll[i%len(gridAll)]}
		if i%50 == 0 {
			pos = gridAll
		}
		frames[i] = gridFrame(t, uint32(i+1), i%50 == 0, 300, 200, byte(i), pos...)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	var taken []*upFrame
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if f := l.take(); f != nil {
				taken = append(taken, f)
			}
			select {
			case <-done:
				if f := l.take(); f != nil {
					taken = append(taken, f)
				}
				return
			default:
			}
		}
	}()
	for _, f := range frames {
		l.put(f)
	}
	close(done)
	wg.Wait()
	var last uint32
	for _, f := range taken {
		if err := checkParts(f.parts); err != nil {
			t.Fatalf("taken frame %d: %v", f.seq, err)
		}
		if f.seq <= last {
			t.Fatalf("seq went back: %d after %d", f.seq, last)
		}
		last = f.seq
	}
	if last != 200 || asked.Load() != 0 {
		t.Fatalf("last taken %d, keyframe requests %d", last, asked.Load())
	}
}

// brokenSplit — нарезка, которая отдаёт части без магии RSF1: сумма не пройдёт checkParts.
func brokenSplit(t *testing.T) {
	t.Helper()
	orig := composeSplit
	composeSplit = func(seq uint32, key bool, w, h uint16, tiles []screenframe.Tile, max int) ([][]byte, error) {
		parts, err := orig(seq, key, w, h, tiles, max)
		for _, p := range parts {
			p[0] = 'X'
		}
		return parts, err
	}
	t.Cleanup(func() { composeSplit = orig })
}

func TestBadComposedDeltaDropsBothAndRequestsKeyframe(t *testing.T) {
	brokenSplit(t)
	var asked int
	l := newLatestFrame(func() { asked++ })
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}))
	if !l.put(gridFrame(t, 2, false, 300, 200, 2, gridAll...)) {
		t.Fatal("drop not reported")
	}
	if asked != 1 || l.take() != nil {
		t.Fatalf("asked %d, both frames must be dropped", asked)
	}
	if d, e := l.counters(); d != 0 || e != 1 {
		t.Fatalf("dropped %d errors %d, want 0/1", d, e)
	}
	// До ключевого кадра дельты выбрасываются; ключевой (одиночный, без сложения) принимается.
	if !l.put(gridFrame(t, 3, false, 300, 200, 3, tilePos{0, 0})) || l.take() != nil {
		t.Fatal("delta accepted before keyframe")
	}
	key := gridFrame(t, 4, true, 300, 200, 4, gridAll...)
	if l.put(key) || l.take() != key {
		t.Fatal("keyframe not accepted")
	}
	if d, e := l.counters(); d != 1 || e != 1 || asked != 1 {
		t.Fatalf("dropped %d errors %d asked %d", d, e, asked)
	}
}

func TestBadComposedKeyframeKeepsNewKeyframe(t *testing.T) {
	brokenSplit(t)
	var asked int
	l := newLatestFrame(func() { asked++ })
	l.put(gridFrame(t, 1, false, 300, 200, 1, tilePos{0, 0}))
	key := gridFrame(t, 2, true, 300, 200, 2, gridAll...)
	if !l.put(key) {
		t.Fatal("dropping the old frame must be reported")
	}
	if got := l.take(); got != key {
		t.Fatalf("new keyframe not kept: %v", got)
	}
	if d, e := l.counters(); d != 0 || e != 1 || asked != 0 {
		t.Fatalf("dropped %d errors %d asked %d, want 0/1/0", d, e, asked)
	}
	// Ключевой кадр принят — следующая дельта не выбрасывается.
	delta := gridFrame(t, 3, false, 300, 200, 3, tilePos{0, 0})
	if l.put(delta) || l.take() != delta {
		t.Fatal("delta after kept keyframe was dropped")
	}
}

func TestLatestFrameReadySignal(t *testing.T) {
	l := newLatestFrame(nil)
	l.put(gridFrame(t, 1, true, 300, 200, 1, gridAll...))
	select {
	case <-l.Ready():
	default:
		t.Fatal("put left a frame pending but sent no signal")
	}
	// Два put подряд без читателя не блокируются: ёмкость 1, лишний сигнал отбрасывается.
	f2 := gridFrame(t, 2, false, 300, 200, 2, tilePos{0, 0})
	f3 := gridFrame(t, 3, false, 300, 200, 3, tilePos{128, 0})
	done := make(chan struct{})
	go func() {
		l.put(f2)
		l.put(f3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consecutive puts blocked")
	}
	if l.take() == nil {
		t.Fatal("composed frame missing")
	}
	<-l.Ready() // сигнал от put, лежавший в канале
	select {
	case <-l.Ready():
		t.Fatal("take produced a new signal without put")
	default:
	}
}
