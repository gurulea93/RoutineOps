//go:build screen

package screen

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Floodww/RoutineOps/internal/screenframe"
)

// Композиция кадров для «последний кадр побеждает».
//
// Захватчик считает дельту тайлов относительно ПРЕДЫДУЩЕГО ЗАХВАЧЕННОГО кадра
// (TileEncoder.Encode), а не отправленного. Поэтому выброшенный кадр нельзя просто
// забыть: тайлы, изменившиеся только в нём, не придут больше никогда. Вместо выброса
// два кадра складываются: тайл — полный JPEG своего прямоугольника на фиксированной
// сетке, и наложение «новый поверх старого» по позиции даёт ровно состояние экрана на
// момент нового кадра.

// errGridMismatch — кадры нельзя сложить: разная геометрия или тайлы не на общей сетке
// (например, сменилось разрешение). Резерв — запрос ключевого кадра.
var errGridMismatch = errors.New("screen: frames are not on the same tile grid")

// composeSplit — нарезка суммы; переменная, чтобы тест мог подставить битую нарезку и
// проверить ветку «сумма не прошла checkParts».
var composeSplit = screenframe.Split

type tilePos struct{ x, y uint16 }

// frameTiles разбирает части кадра в тайлы и проверяет, что все они на сетке TileSize
// геометрии w×h. JPEG тайлов ссылается на payload частей, без копий.
func frameTiles(f *upFrame) (w, h uint16, tiles map[tilePos]screenframe.Tile, err error) {
	tiles = map[tilePos]screenframe.Tile{}
	for i, p := range f.parts {
		part, err := screenframe.ParsePart(p.Payload)
		if err != nil {
			return 0, 0, nil, err
		}
		if part.VP8 {
			// Кадры VP8 зависят от предыдущих: сложить их нельзя. Ожидающий выбрасывается,
			// дельты до ключевого кадра — тоже (ветка errGridMismatch в latestFrame).
			return 0, 0, nil, fmt.Errorf("%w: vp8 frames do not compose", errGridMismatch)
		}
		if i == 0 {
			w, h = part.Width, part.Height
		} else if part.Width != w || part.Height != h {
			return 0, 0, nil, fmt.Errorf("%w: geometry changes inside a frame", errGridMismatch)
		}
		for _, t := range part.Tiles {
			if !onGrid(t, int(w), int(h)) {
				return 0, 0, nil, fmt.Errorf("%w: tile %dx%d at %d,%d", errGridMismatch, t.W, t.H, t.X, t.Y)
			}
			tiles[tilePos{t.X, t.Y}] = t
		}
	}
	return w, h, tiles, nil
}

// onGrid — тайл стоит в своей клетке сетки и покрывает её целиком (краевые — до края).
func onGrid(t screenframe.Tile, w, h int) bool {
	x, y := int(t.X), int(t.Y)
	return x%TileSize == 0 && y%TileSize == 0 && x < w && y < h &&
		int(t.W) == min(TileSize, w-x) && int(t.H) == min(TileSize, h-y)
}

// compose складывает старый кадр с новым: на одной позиции побеждает новый тайл,
// позиции из одного кадра сохраняются. seq — нового, keyframe = старый || новый.
// Результат заново режется screenframe.Split; вызывающий обязан прогнать его через
// checkParts. Ошибка — складывать нельзя (errGridMismatch) или разбор/нарезка не удались.
func compose(old, nu *upFrame) (*upFrame, error) {
	ow, oh, ot, err := frameTiles(old)
	if err != nil {
		return nil, err
	}
	nw, nh, nt, err := frameTiles(nu)
	if err != nil {
		return nil, err
	}
	if ow != nw || oh != nh {
		return nil, fmt.Errorf("%w: %dx%d vs %dx%d", errGridMismatch, ow, oh, nw, nh)
	}
	for pos, t := range nt {
		ot[pos] = t
	}
	merged := make([]screenframe.Tile, 0, len(ot))
	for _, t := range ot {
		merged = append(merged, t)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Y != merged[j].Y {
			return merged[i].Y < merged[j].Y
		}
		return merged[i].X < merged[j].X
	})
	key := old.key || nu.key
	raw, err := composeSplit(nu.seq, key, nw, nh, merged, screenframe.MaxPartBytes)
	if err != nil {
		return nil, err
	}
	captured := nu.parts[len(nu.parts)-1].CapturedMs
	out := &upFrame{seq: nu.seq, key: key, parts: make([]wireFrame, len(raw))}
	for i, p := range raw {
		out.parts[i] = wireFrame{Seq: nu.seq, Keyframe: key, Last: i == len(raw)-1, CapturedMs: captured, Payload: p}
	}
	return out, nil
}
