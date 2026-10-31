//go:build screen

package screen

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Канал служба ↔ захватчик — stdin/stdout процесса screen-worker.
//
// Почему пайпы, а не сокет на диске: сокет в общем каталоге — это точка, к которой может
// подключиться любой процесс сессии и выдать себя за захватчик (или за службу). Пайп
// наследуется ровно одним дочерним процессом, и выдать себя за другую сторону нечем.
//
// Сообщение: [u8 type][u32 len][body]. Кадр: body = [u32 seq][u8 flags][i64 captured_ms][RSF1].
// Прочие — JSON: они редкие, и читаемость в отладке важнее байтов.

const (
	msgHello    byte = 1 // worker → служба: геометрия, профиль
	msgFrame    byte = 2 // worker → служба: часть кадра
	msgEnd      byte = 3 // worker → служба: конец сеанса с причиной
	msgStats    byte = 4 // worker → служба: счётчики
	msgSignal   byte = 5 // worker → служба: pb.SessionSignal (WebRTC, Этап B) — уйдёт в SessionUp.signal
	msgKeyframe byte = 10
	msgStop     byte = 11
	msgSignalIn byte = 16 // служба → worker: pb.SessionSignal из SessionDown.signal
	msgProfile  byte = 15 // служба → worker: JSON wireProfile — сменить профиль качества на лету
	msgDisplay  byte = 14 // служба → worker: JSON wireDisplay — показать монитор с номером index
	msgInput    byte = 13 // служба → worker: pb.SessionInput (protobuf) — ввод оператора
)

// maxWireMsg — потолок сообщения: часть кадра плюс заголовки. Больше — протокольная ошибка,
// а не аллокация по чужому числу.
const maxWireMsg = screenframe.MaxPartBytes + 64

var errWire = errors.New("screen: worker channel protocol error")

type wireHello struct {
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Profile         string `json:"profile"`
	ProfileFallback bool   `json:"profile_fallback"`
}

type wireEnd struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type wireStats struct {
	Frames  uint32 `json:"frames"`
	Bytes   uint64 `json:"bytes"`
	Stalls  uint32 `json:"stalls"`
	Dropped uint32 `json:"dropped"`
}

type wireProfile struct {
	Name string `json:"name"`
}

type wireDisplay struct {
	Index int `json:"index"`
}

type wireStop struct {
	Reason string `json:"reason"`
}

type wireFrame struct {
	Seq        uint32
	Keyframe   bool
	Last       bool
	CapturedMs int64
	Payload    []byte
}

func writeMsg(w io.Writer, typ byte, body []byte) error {
	if len(body) > maxWireMsg {
		return fmt.Errorf("%w: message %d B", errWire, len(body))
	}
	// Одним Write: заголовок и тело не должны разрываться чужим сообщением, когда в out
	// пишут несколько горутин (петля кадров и файловый менеджер, см. lockedWriter).
	buf := make([]byte, 5, 5+len(body))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(len(body)))
	_, err := w.Write(append(buf, body...))
	return err
}

// lockedWriter сериализует Write: каждое сообщение — один Write (writeMsg), поэтому
// сообщения разных горутин не перемешиваются.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func writeJSONMsg(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeMsg(w, typ, b)
}

func writeFrameMsg(w io.Writer, f wireFrame) error {
	body := make([]byte, 13, 13+len(f.Payload))
	binary.BigEndian.PutUint32(body, f.Seq)
	if f.Keyframe {
		body[4] |= 1
	}
	if f.Last {
		body[4] |= 2
	}
	binary.BigEndian.PutUint64(body[5:], uint64(f.CapturedMs))
	return writeMsg(w, msgFrame, append(body, f.Payload...))
}

func readMsg(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if uint64(n) > maxWireMsg {
		return 0, nil, fmt.Errorf("%w: declared %d B", errWire, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr[0], body, nil
}

func parseFrameMsg(body []byte) (wireFrame, error) {
	if len(body) < 13 {
		return wireFrame{}, fmt.Errorf("%w: short frame", errWire)
	}
	return wireFrame{
		Seq:        binary.BigEndian.Uint32(body),
		Keyframe:   body[4]&1 != 0,
		Last:       body[4]&2 != 0,
		CapturedMs: int64(binary.BigEndian.Uint64(body[5:])),
		Payload:    body[13:],
	}, nil
}
