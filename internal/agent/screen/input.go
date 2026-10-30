//go:build screen

package screen

import pb "github.com/Floodww/RoutineOps/proto"

// Injector — внедрение ввода оператора в сеанс сотрудника. Живёт в процессе захвата
// (screen-worker): тот работает в графической сессии, и только оттуда ввод попадает на
// рабочий стол сотрудника.
//
// Правила, одинаковые для всех платформ:
//   - событие, которого не знает эта версия, ПРОПУСКАЕТСЯ, а не исполняется как другое;
//   - координаты приходят в пикселях кадра оператора (frame_width × frame_height) и
//     пересчитываются здесь: кадр может быть уменьшен профилем, а экран — нет;
//   - ReleaseAll отпускает всё, что оператор зажал и не отпустил: залипший Ctrl или кнопка
//     мыши остались бы на машине сотрудника.
type Injector interface {
	Apply(in *pb.SessionInput)
	ReleaseAll()
	Close()
}

// inputAvailable — умеет ли эта сборка внедрять ввод. Определяет и способность
// screen_control, и отказ INPUT_UNAVAILABLE на приглашение с управлением.
func inputAvailable() bool { return platformInputAvailable }

// InputAvailable — экспорт для объявления способности screen_control в cmd/agent.
func InputAvailable() bool { return inputAvailable() }

// DisplayAware — инжектор, умеющий переключать целевой монитор вместе с захватом.
type DisplayAware interface{ SetDisplay(idx int) }
