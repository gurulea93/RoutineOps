//go:build screen

package screen

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Floodww/RoutineOps/internal/screenframe"
	pb "github.com/Floodww/RoutineOps/proto"
)

// part собирает часть кадра с одним тайлом заданного размера.
func part(t *testing.T, seq uint32, key, last bool, size int) (*screenframe.Part, []byte) {
	t.Helper()
	p := &screenframe.Part{Seq: seq, Keyframe: key, Last: last, Width: 640, Height: 480,
		Tiles: []screenframe.Tile{{W: 64, H: 64, JPEG: make([]byte, size)}}}
	b, err := screenframe.AppendPart(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	return p, b
}

func pushFrame(t *testing.T, h *hub, seq uint32, key bool) {
	t.Helper()
	p, b := part(t, seq, key, true, 10)
	h.append(p, appendRecord(seq, b))
}

func records(t *testing.T, body []byte) []screenframe.Record {
	t.Helper()
	recs, err := screenframe.ParseRecords(body)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func seqOf(t *testing.T, r screenframe.Record) uint32 {
	p, err := screenframe.ParsePart(r.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return p.Seq
}

func TestHubStartsAtKeyframeAndFollowsCursor(t *testing.T) {
	h := newHub(640, 480)
	pushFrame(t, h, 1, false) // дельта до ключевого — рисовать не на чем
	pushFrame(t, h, 2, true)
	pushFrame(t, h, 3, false)

	res := h.poll(context.Background(), 0, 0)
	recs := records(t, res.Body)
	if len(recs) != 2 || seqOf(t, recs[0]) != 2 || seqOf(t, recs[1]) != 3 {
		t.Fatalf("first poll: %d records", len(recs))
	}
	if res.W != 640 || res.H != 480 || res.Closed {
		t.Fatalf("meta %+v", res)
	}

	pushFrame(t, h, 4, false)
	res2 := h.poll(context.Background(), res.Next, 0)
	if recs := records(t, res2.Body); len(recs) != 1 || seqOf(t, recs[0]) != 4 {
		t.Fatalf("follow-up poll: %d records", len(recs))
	}

	// Новый ключевой кадр сбрасывает буфер; отставший курсор начинает с него.
	pushFrame(t, h, 5, true)
	res3 := h.poll(context.Background(), res.Next, 0)
	if recs := records(t, res3.Body); len(recs) != 1 || seqOf(t, recs[0]) != 5 {
		t.Fatalf("lagging viewer must restart at keyframe, got %d records", len(recs))
	}
}

func TestHubLongPollWakesAndTimesOut(t *testing.T) {
	h := newHub(640, 480)
	pushFrame(t, h, 1, true)
	first := h.poll(context.Background(), 0, 0)

	start := time.Now()
	empty := h.poll(context.Background(), first.Next, 50*time.Millisecond)
	if len(empty.Body) != 0 || empty.Next != first.Next || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("timeout poll: %d bytes after %v", len(empty.Body), time.Since(start))
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		pushFrame(t, h, 2, false)
	}()
	got := h.poll(context.Background(), first.Next, 5*time.Second)
	if recs := records(t, got.Body); len(recs) != 1 {
		t.Fatalf("woken poll: %d records", len(recs))
	}
}

func TestHubCloseReportedAfterDrain(t *testing.T) {
	h := newHub(640, 480)
	pushFrame(t, h, 1, true)
	h.close("OPERATOR_STOPPED")
	pushFrame(t, h, 2, false) // после закрытия не принимается

	res := h.poll(context.Background(), 0, time.Second)
	if !res.Closed || res.EndReason != "OPERATOR_STOPPED" || len(records(t, res.Body)) != 1 {
		t.Fatalf("closed poll %+v", res)
	}
}

func TestHubOverflowDropsUntilKeyframe(t *testing.T) {
	h := newHub(640, 480)
	pushFrame(t, h, 1, true)
	p, b := part(t, 2, false, true, 200<<10)
	for i := 0; i < hubMaxBuffer/(200<<10)+2; i++ {
		h.append(p, appendRecord(0, b))
	}
	select {
	case <-h.keyframe:
	default:
		t.Fatal("overflow must request a keyframe")
	}
	if res := h.poll(context.Background(), 0, 0); len(res.Body) != 0 {
		t.Fatalf("after overflow nothing is drawable, got %d bytes", len(res.Body))
	}
	pushFrame(t, h, 9, true)
	if recs := records(t, h.poll(context.Background(), 0, 0).Body); len(recs) != 1 || seqOf(t, recs[0]) != 9 {
		t.Fatal("keyframe after overflow not delivered")
	}
}

func TestHubResponseCap(t *testing.T) {
	h := newHub(640, 480)
	p, b := part(t, 1, true, false, 200<<10)
	for i := 0; i < hubMaxResponse/(200<<10)+5; i++ {
		h.append(p, appendRecord(0, b))
	}
	res := h.poll(context.Background(), 0, 0)
	if len(res.Body) > hubMaxResponse {
		t.Fatalf("response %d B over cap", len(res.Body))
	}
	rest := h.poll(context.Background(), res.Next, 0)
	if len(rest.Body) == 0 {
		t.Fatal("remainder not delivered on next poll")
	}
}

func TestHubFlowChangeWakesPoll(t *testing.T) {
	h := newHub(640, 480)
	pushFrame(t, h, 1, true)
	first := h.poll(context.Background(), 0, 0)
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.setFlow(true, "SECURE_DESKTOP")
	}()
	res := h.poll(context.Background(), first.Next, 5*time.Second)
	if !res.Paused || res.PauseWhy != "SECURE_DESKTOP" {
		t.Fatalf("flow %+v", res)
	}
}

func TestHubsSingleStreamPerSession(t *testing.T) {
	var hs hubs
	a, b := newHub(1, 1), newHub(1, 1)
	if !hs.put("s", a) || hs.put("s", b) {
		t.Fatal("second stream for the same session accepted")
	}
	hs.remove("s", b) // чужой хаб не снимает
	if hs.get("s") != a {
		t.Fatal("foreign remove dropped the live hub")
	}
	hs.remove("s", a)
	if hs.get("s") != nil {
		t.Fatal("hub not removed")
	}
}

const testSessionID = "0b9d7c1e-4a43-4f6e-9a55-2f1e8f0d6c11"

func TestRecorderSkipsUntilKeyframeAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	r, err := newRSFRecorder(dir, testSessionID, 1<<20, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		seq uint32
		key bool
	}{{1, false}, {2, true}, {3, false}} {
		p, b := part(t, f.seq, f.key, true, 100)
		if err := r.write(p, appendRecord(f.seq*100, b)); err != nil {
			t.Fatal(err)
		}
	}
	name, n, trunc := r.close()
	if trunc != "" || name != testSessionID+".rsf" {
		t.Fatalf("close: %q %q", name, trunc)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != n {
		t.Fatalf("size %d, reported %d", len(data), n)
	}
	recs := records(t, data)
	if len(recs) != 2 || seqOf(t, recs[0]) != 2 || recs[1].RelMs != 300 {
		t.Fatalf("recording records %d", len(recs))
	}
	if fi, _ := os.Stat(filepath.Join(dir, name)); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("recording is readable by others: %v", fi.Mode())
	}
}

func TestRecorderQuotaReasons(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sessionMax  int64
		tenantLeft  int64
		wantTrunc   string
		wantEndCode string
	}{
		{"session cap", 1000, 1 << 30, truncQuota, "RECORDING_QUOTA"},
		{"tenant cap", 1 << 30, 1000, truncTenantQuota, "RECORDING_QUOTA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRSFRecorder(t.TempDir(), testSessionID, tc.sessionMax, tc.tenantLeft)
			if err != nil {
				t.Fatal(err)
			}
			p, b := part(t, 1, true, true, 400)
			rec := appendRecord(0, b)
			var werr error
			for i := 0; i < 5 && werr == nil; i++ {
				werr = r.write(p, rec)
			}
			if !errors.Is(werr, errRecordingStopped) || r.truncated != tc.wantTrunc {
				t.Fatalf("err %v, truncated %q", werr, r.truncated)
			}
			if truncationEndReason(r.truncated) != tc.wantEndCode {
				t.Fatal("end reason")
			}
			if _, n, _ := r.close(); n > min(tc.sessionMax, tc.tenantLeft) {
				t.Fatalf("wrote %d over limit", n)
			}
		})
	}
}

func TestRecorderEmptyFileRemoved(t *testing.T) {
	dir := t.TempDir()
	r, err := newRSFRecorder(dir, testSessionID, 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if name, _, _ := r.close(); name != "" {
		t.Fatalf("empty recording kept as %q", name)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("empty recording file left on disk")
	}
}

func TestRecordingPathsStayInsideDir(t *testing.T) {
	for _, bad := range []string{"../x.rsf", "a/b.rsf", "", ".", ".."} {
		if _, err := recordingPath("/rec", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := recordingName("../../etc/passwd"); err == nil {
		t.Fatal("non-uuid session id accepted as file name")
	}
}

// frame() — проверка частей от агента до записи и хаба.
func TestStreamFrameValidation(t *testing.T) {
	rec, err := newRSFRecorder(t.TempDir(), testSessionID, 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.close()
	r := &streamRun{s: &Service{log: discardLogger()}, sess: &SessionRow{ID: testSessionID},
		hub: newHub(640, 480), rec: rec, started: time.Now()}

	_, good := part(t, 7, true, true, 50)
	if reason := r.frame(&pb.SessionFrame{Seq: 7, Keyframe: true, LastPart: true, Payload: good}); reason != "" {
		t.Fatalf("valid frame refused: %s", reason)
	}
	if r.c.Frames != 1 || r.c.Bytes != int64(len(good)) {
		t.Fatalf("counters %+v", r.c)
	}
	for name, f := range map[string]*pb.SessionFrame{
		"seq mismatch":  {Seq: 8, Keyframe: true, LastPart: true, Payload: good},
		"flag mismatch": {Seq: 7, Keyframe: false, LastPart: true, Payload: good},
		"garbage":       {Seq: 7, Payload: []byte("not a frame")},
		"oversize":      {Seq: 7, Payload: make([]byte, screenframe.MaxPartBytes+1)},
	} {
		if reason := r.frame(f); reason != "PROTOCOL_ERROR" {
			t.Errorf("%s: reason %q", name, reason)
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
