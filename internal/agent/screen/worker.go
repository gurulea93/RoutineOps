//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Floodww/RoutineOps/internal/screenframe"
	pb "github.com/Floodww/RoutineOps/proto"
)

// Процесс захвата (agent screen-worker). Живёт в графической сессии сотрудника, от его
// имени; со службой говорит только через stdin/stdout (wire.go).
//
// Порядок старта неизменен: захватчик открыт → плашка «экран просматривается» поднята →
// hello. Кадр без плашки невозможен по построению: до hello служба не открывает стрим к
// серверу, а плашка, которую не удалось показать, — это отказ NOTICE_UNAVAILABLE.

// Notice — плашка наблюдения. Close снимает её; до Close она обязана держаться поверх окон.
type Notice interface{ Close() }

// WorkerConfig — параметры процесса захвата.
type WorkerConfig struct {
	Profile     string
	Operator    string
	Reason      string
	MaxDuration time.Duration // 0 → час

	// KeyframeEvery — периодический ключевой кадр: страхует зрителя от накопленной
	// ошибки, даже если запрос ключевого кадра потерялся. 0 → 30 с.
	KeyframeEvery time.Duration

	// AllowInput — сеанс с управлением. Без NewInjector (или если тот отказал) сеанс
	// закрывается INPUT_UNAVAILABLE, а не понижается молча до просмотра.
	AllowInput  bool
	NewInjector func(srcW, srcH int) (Injector, error)

	NewCapturer func() (Capturer, error)

	// Выбор монитора (необязательно). Displays — сколько мониторов у машины, NewCapturerAt —
	// захват монитора по номеру. Без них переключение молча игнорируется: экран один.
	Transport string // "webrtc" → RunWorker уходит в runWebRTCWorker (VP8); иначе RSF1
	Codec     string
	ICE       []*pb.IceServer

	Displays      func() int
	NewCapturerAt func(idx int) (Capturer, error)

	ShowNotice func(operator, reason string) (Notice, error)
}

// RunWorker ведёт сеанс до конца и возвращает его причину. Последнее сообщение в out —
// всегда msgEnd (если out ещё пишется).
func RunWorker(ctx context.Context, cfg WorkerConfig, in io.Reader, out io.Writer) EndReason {
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = time.Hour
	}
	if cfg.KeyframeEvery <= 0 {
		cfg.KeyframeEvery = 30 * time.Second
	}
	end := func(r EndReason, detail string) EndReason {
		_ = writeJSONMsg(out, msgEnd, wireEnd{Reason: string(r), Detail: detail})
		return r
	}

	if cfg.NewCapturer == nil || cfg.ShowNotice == nil {
		return end(ReasonCaptureUnsupported, "worker built without capture/notice")
	}
	capt, err := cfg.NewCapturer()
	if err != nil {
		return end(reasonOf(err, ReasonCaptureFailed), err.Error())
	}
	captOwned := false // WebRTC-путь отдаёт захватчик Streamer'у, тот закрывает его сам
	defer func() {
		if !captOwned {
			_ = capt.Close() // capt меняется при переключении монитора
		}
	}()

	notice, err := cfg.ShowNotice(cfg.Operator, cfg.Reason)
	if err != nil {
		return end(ReasonNoticeUnavailable, err.Error())
	}
	defer notice.Close()

	srcW, srcH := capt.Size()
	var inj Injector
	if cfg.AllowInput {
		if cfg.NewInjector == nil {
			return end(ReasonInputUnavailable, "worker built without input injection")
		}
		var ierr error
		if inj, ierr = cfg.NewInjector(srcW, srcH); ierr != nil {
			return end(ReasonInputUnavailable, ierr.Error())
		}
		defer inj.Close() // отпускает всё зажатое при любом конце сеанса
	}
	if cfg.Transport == "webrtc" {
		captOwned = true
		return runWebRTCWorker(ctx, cfg, capt, inj, in, out)
	}
	prof, fallback := ProfileByName(cfg.Profile)
	enc := NewTileEncoder(srcW, srcH, prof)
	if err := writeJSONMsg(out, msgHello, wireHello{
		Width: enc.W, Height: enc.H, Profile: prof.Name, ProfileFallback: fallback,
	}); err != nil {
		return ReasonWorkerLost
	}

	ctl := readControl(in, inj)
	tick := time.NewTicker(time.Second / time.Duration(prof.FPS))
	defer tick.Stop()
	deadline := time.NewTimer(cfg.MaxDuration)
	defer deadline.Stop()
	statsTick := time.NewTicker(10 * time.Second)
	defer statsTick.Stop()

	var (
		seq     uint32
		lastKey time.Time
		wantKey = true
		stats   wireStats
		curDisp int
		nDisp   = 1
	)
	if cfg.Displays != nil {
		nDisp = max(1, cfg.Displays())
	}
	for {
		select {
		case <-ctx.Done():
			return end(ReasonWorkerLost, "worker context done")
		case <-deadline.C:
			return end(ReasonMaxDuration, "")
		case c, ok := <-ctl.ch:
			if !ok {
				// Служба закрыла канал (умерла или закрыла нас). Писать некому.
				return ReasonWorkerLost
			}
			if c.stop != "" {
				return end(EndReason(c.stop), "")
			}
			if c.profile != "" {
				if np, _ := ProfileByName(c.profile); np.Name != prof.Name {
					prof = np
					enc = NewTileEncoder(srcW, srcH, prof)
					tick.Reset(time.Second / time.Duration(prof.FPS))
				}
			}
			if c.display >= 0 && c.display != curDisp && c.display < nDisp && cfg.NewCapturerAt != nil {
				if nc, err := cfg.NewCapturerAt(c.display); err == nil {
					_ = capt.Close()
					capt, curDisp = nc, c.display
					srcW, srcH = capt.Size()
					enc = NewTileEncoder(srcW, srcH, prof)
					if da, ok := inj.(DisplayAware); ok {
						da.SetDisplay(curDisp)
					}
				}
			}
			wantKey = true
		case <-statsTick.C:
			if err := writeJSONMsg(out, msgStats, stats); err != nil {
				return ReasonWorkerLost
			}
		case <-tick.C:
			pix, stride, err := capt.Capture()
			if err != nil {
				return end(reasonOf(err, ReasonCaptureFailed), err.Error())
			}
			key := wantKey || time.Since(lastKey) >= cfg.KeyframeEvery
			tiles, err := enc.Encode(pix, stride, key)
			if err != nil {
				return end(ReasonCaptureFailed, "jpeg: "+err.Error())
			}
			if key && cfg.Displays != nil {
				nDisp = max(1, cfg.Displays())
			}
			seq++
			parts, err := screenframe.Split(seq, key, uint16(enc.W), uint16(enc.H), tiles, screenframe.MaxPartBytes)
			if err != nil {
				return end(ReasonProtocolError, err.Error())
			}
			now := time.Now().UnixMilli()
			for i, p := range parts {
				screenframe.SetDisplayInfo(p, nDisp, curDisp)
				if err := writeFrameMsg(out, wireFrame{Seq: seq, Keyframe: key, Last: i == len(parts)-1,
					CapturedMs: now, Payload: p}); err != nil {
					return ReasonWorkerLost
				}
				stats.Bytes += uint64(len(p))
			}
			stats.Frames++
			if key {
				wantKey, lastKey = false, time.Now()
			}
		}
	}
}

func reasonOf(err error, def EndReason) EndReason {
	var re *ReasonError
	if errors.As(err, &re) {
		return re.Reason
	}
	return def
}

type control struct {
	stop    string
	profile string // непусто — сменить профиль качества
	display int    // ≥0 — перейти на монитор с этим номером; −1 — не меняется
}

type controlReader struct{ ch chan control }

// readControl читает команды службы. Канал закрывается на EOF/ошибке.
// Ввод исполняется прямо в читающей горутине, а не через канал: цикл кадров занят захватом
// десятки миллисекунд, и ввод, ждущий очереди за кадром, давал бы заметный лаг мыши.
func readControl(in io.Reader, inj Injector) *controlReader {
	cr := &controlReader{ch: make(chan control, 4)}
	go func() {
		defer close(cr.ch)
		for {
			typ, body, err := readMsg(in)
			if err != nil {
				return
			}
			switch typ {
			case msgKeyframe:
				select {
				case cr.ch <- control{display: -1}:
				default: // запрос уже в очереди
				}
			case msgInput:
				if inj == nil {
					continue // ввод не объявлялся приглашением: не исполняем
				}
				var si pb.SessionInput
				if proto.Unmarshal(body, &si) == nil {
					inj.Apply(&si)
				}
			case msgDisplay:
				var d wireDisplay
				if json.Unmarshal(body, &d) == nil && d.Index >= 0 {
					select {
					case cr.ch <- control{display: d.Index}:
					default: // очередь полна — оператор нажмёт ещё раз
					}
				}
			case msgProfile:
				var pf wireProfile
				if json.Unmarshal(body, &pf) == nil && pf.Name != "" {
					select {
					case cr.ch <- control{profile: pf.Name, display: -1}:
					default:
					}
				}
			case msgStop:
				var s wireStop
				_ = json.Unmarshal(body, &s)
				if s.Reason == "" {
					s.Reason = string(ReasonStopped)
				}
				cr.ch <- control{stop: s.Reason, display: -1}
				return
			}
		}
	}()
	return cr
}
