//go:build screen

package screen

import (
	"sync"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Буфер обмена в сеансе с управлением, в обе стороны.
//
// Оператор → сотрудник: событие ввода kind="clipboard" (агенты старее его пропускают, как
// любое незнакомое событие), текст ставится в буфер, следом приходит Ctrl+V.
// Сотрудник → оператор: захватчик замечает смену буфера (номер последовательности) и шлёт
// текст наверх сигналом SessionSignal.ice с sdp_mid="clipboard" — proto не меняется,
// сервер узнаёт такой сигнал по метке и отдаёт текст браузеру.

// ClipboardMid — метка сигнала с текстом буфера (SessionSignal.ice.sdp_mid).
const ClipboardMid = "clipboard"

// maxClipboardRunes — потолок текста в одну сторону. Сообщение службе ограничено
// maxWireMsg (256 КБ); 32К символов в UTF-8 гарантированно меньше.
const maxClipboardRunes = 32 << 10

var (
	clipMu      sync.Mutex
	clipLastSet string // что поставил оператор: обратно ему не отсылаем
)

// applyClipboard — событие от оператора: поставить текст в буфер сотрудника.
func applyClipboard(text string) {
	if text == "" || utf8.RuneCountInString(text) > maxClipboardRunes {
		return
	}
	clipMu.Lock()
	clipLastSet = text
	clipMu.Unlock()
	_ = setClipboardText(text)
}

// clipboardWatch — опрос буфера сотрудника из петли кадров (не отдельной горутиной: в out
// пишет только петля).
type clipboardWatch struct {
	seq uint32
}

func newClipboardWatch() *clipboardWatch { return &clipboardWatch{seq: clipboardSeq()} }

// poll возвращает тело msgSignal, если буфер сменился и в нём новый текст; иначе nil.
func (c *clipboardWatch) poll() []byte {
	s := clipboardSeq()
	if s == 0 || s == c.seq {
		return nil
	}
	c.seq = s
	text, err := getClipboardText(maxClipboardRunes)
	if err != nil || text == "" {
		return nil
	}
	clipMu.Lock()
	echo := text == clipLastSet
	clipMu.Unlock()
	if echo {
		return nil
	}
	b, err := proto.Marshal(&pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{
		Candidate: text, SdpMid: ClipboardMid,
	}}})
	if err != nil || len(b) > maxWireMsg {
		return nil
	}
	return b
}
