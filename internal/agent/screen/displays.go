//go:build screen

package screen

import (
	"errors"
	"sort"

	pb "github.com/Floodww/RoutineOps/proto"
)

var (
	errNoMultiDisplay = errors.New("display switching is not implemented on this OS")
	errNoSuchDisplay  = errors.New("no such display")
)

// Мониторы машины. Номер 0 — всегда основной (как в приглашении: пустой список displays
// значит «только основной»), остальные идут слева направо, затем сверху вниз.

// maxDisplays — сколько мониторов умеет показать формат кадра (4 бита в RSF1).
const maxDisplays = 15

// inviteDisplays — номера, которые сервер вписывает в приглашение: оператору можно
// переключаться между любыми мониторами машины, плашка сотрудника при этом не меняется.
const inviteDisplays = 8 // сервер: [0..7]

type monitorRect struct {
	X, Y, W, H int
	Primary    bool
}

// orderMonitors ставит основной первым, остальные — по X, затем по Y. Исходный срез не меняется.
func orderMonitors(in []monitorRect) []monitorRect {
	out := append([]monitorRect(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Primary != b.Primary {
			return a.Primary
		}
		if a.X != b.X {
			return a.X < b.X
		}
		return a.Y < b.Y
	})
	if len(out) > maxDisplays {
		out = out[:maxDisplays]
	}
	return out
}

// virtualPos переводит пиксель кадра монитора rect в абсолютные координаты SendInput
// (0..65535) по виртуальному рабочему столу [vx,vy,vw,vh] — для флага MOUSEEVENTF_VIRTUALDESK.
func virtualPos(rect monitorRect, vx, vy, vw, vh, cx, cy, fw, fh int) (x, y int32, ok bool) {
	if fw < 2 || fh < 2 || vw < 2 || vh < 2 || rect.W < 1 || rect.H < 1 {
		return 0, 0, false
	}
	cx, cy = min(max(cx, 0), fw-1), min(max(cy, 0), fh-1)
	px := rect.X + cx*(rect.W-1)/(fw-1)
	py := rect.Y + cy*(rect.H-1)/(fh-1)
	nx := (px - vx) * 65535 / (vw - 1)
	ny := (py - vy) * 65535 / (vh - 1)
	return int32(min(max(nx, 0), 65535)), int32(min(max(ny, 0), 65535)), true
}

// Коды профилей в ScreenScope.displays: profileCodeBase+1 low, +2 medium, +3 high.
const profileCodeBase = 100

var profileByCode = map[int32]string{profileCodeBase + 1: "low", profileCodeBase + 2: "medium", profileCodeBase + 3: "high"}

// splitProfileCode отделяет коды профилей от номеров экранов. Исходное сообщение не меняется.
func splitProfileCode(in *pb.ScreenScope) (*pb.ScreenScope, string) {
	var (
		keep    []int32
		profile string
	)
	for _, d := range in.GetDisplays() {
		if d >= profileCodeBase {
			if n, ok := profileByCode[d]; ok {
				profile = n
			}
			continue
		}
		keep = append(keep, d)
	}
	if profile == "" && len(keep) == len(in.GetDisplays()) {
		return in, ""
	}
	return &pb.ScreenScope{Displays: keep, AllowInput: in.GetAllowInput()}, profile
}
