//go:build screen

package screen

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

// WebRTC-путь worker'а (Этап B). Захват, кодер VP8 и PeerConnection живут здесь, в сеансе
// сотрудника; служба только перекладывает сигнализацию между стримом сеанса и каналом
// worker'а (msgSignal / msgSignalIn). Кадры идут по UDP сразу на сервер (relay), а не через
// канал службы.

// rtcParams — потолки потока по имени профиля.
type rtcParams struct{ fps, kbps, w, h int }

func webrtcParams(profile string) rtcParams {
	switch profile {
	case "ultra":
		return rtcParams{30, 16000, 3840, 2160}
	case "high":
		return rtcParams{30, 6000, 1920, 1080}
	case "medium":
		return rtcParams{20, 2500, 1920, 1080}
	default:
		return rtcParams{ProfileLowFPS, ProfileLowKbps, ProfileLowMaxWidth, ProfileLowMaxHeight}
	}
}

// webrtcTransport — transport из приглашения; пусто и «rsf1» дают RSF1.
func webrtcTransport(cmd *pb.ScreenSessionCommand) string {
	if cmd.GetTransport() == "webrtc" {
		return "webrtc"
	}
	return ""
}

func iceToWire(in []*pb.IceServer) []wireICE {
	var out []wireICE
	for _, s := range in {
		out = append(out, wireICE{URLs: s.GetUrls(), Username: s.GetUsername(), Credential: s.GetCredential()})
	}
	return out
}

func iceFromWire(in []wireICE) []*pb.IceServer {
	var out []*pb.IceServer
	for _, s := range in {
		out = append(out, &pb.IceServer{Urls: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return out
}

func runWebRTCWorker(ctx context.Context, cfg WorkerConfig, capt Capturer, inj Injector, in io.Reader, out io.Writer) EndReason {
	var omu sync.Mutex
	write := func(typ byte, body []byte) error {
		omu.Lock()
		defer omu.Unlock()
		return writeMsg(out, typ, body)
	}
	end := func(r EndReason, detail string) EndReason {
		b, _ := json.Marshal(wireEnd{Reason: string(r), Detail: detail})
		_ = write(msgEnd, b)
		return r
	}

	p := webrtcParams(cfg.Profile)
	st, err := NewStreamer(capt, StreamerConfig{
		Codec: cfg.Codec, FPS: p.fps, BitrateKbps: p.kbps, MaxWidth: p.w, MaxHeight: p.h,
		ICEServers: cfg.ICE,
		SendSignal: func(sig *pb.SessionSignal) error {
			b, err := proto.Marshal(sig)
			if err != nil {
				return err
			}
			return write(msgSignal, b)
		},
	})
	if err != nil {
		return end(reasonOf(err, ReasonEncoderFailed), err.Error())
	}
	defer st.Close()

	prof, fallback := ProfileByName(cfg.Profile)
	w, h := st.Geometry()
	hb, _ := json.Marshal(wireHello{Width: w, Height: h, Profile: prof.Name, ProfileFallback: fallback})
	if err := write(msgHello, hb); err != nil {
		return ReasonWorkerLost
	}

	stopCh := make(chan string, 1)
	inClosed := make(chan struct{})
	go func() {
		defer close(inClosed)
		for {
			typ, body, err := readMsg(in)
			if err != nil {
				return
			}
			switch typ {
			case msgKeyframe:
				_ = st.HandleSignal(&pb.SessionSignal{Msg: &pb.SessionSignal_KeyframeRequest{KeyframeRequest: true}})
			case msgSignalIn:
				var sig pb.SessionSignal
				if proto.Unmarshal(body, &sig) == nil {
					_ = st.HandleSignal(&sig) // ошибку фиксирует сам Streamer: он закроется, и Done сработает
				}
			case msgInput:
				if inj == nil {
					continue
				}
				var si pb.SessionInput
				if proto.Unmarshal(body, &si) == nil {
					inj.Apply(&si)
				}
			case msgStop:
				var s wireStop
				_ = json.Unmarshal(body, &s)
				if s.Reason == "" {
					s.Reason = string(ReasonStopped)
				}
				stopCh <- s.Reason
				return
			}
		}
	}()

	// Оферта собирает ICE до 10 с: основной цикл не должен её ждать.
	go func() { _ = st.Start(ctx) }()

	deadline := time.NewTimer(cfg.MaxDuration)
	defer deadline.Stop()
	statsTick := time.NewTicker(10 * time.Second)
	defer statsTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return end(ReasonWorkerLost, "worker context done")
		case <-deadline.C:
			return end(ReasonMaxDuration, "")
		case <-st.Done():
			return end(reasonOf(st.Err(), ReasonTransportLost), errText(st.Err()))
		case r := <-stopCh:
			return end(EndReason(r), "")
		case <-inClosed:
			return ReasonWorkerLost
		case <-statsTick.C:
			ss := st.Stats()
			b, _ := json.Marshal(wireStats{Frames: ss.Frames, Bytes: ss.Bytes, Dropped: ss.Dropped})
			if err := write(msgStats, b); err != nil {
				return ReasonWorkerLost
			}
		}
	}
}
