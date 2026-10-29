// Package contract проверяет обратную/прямую совместимость proto-контракта на
// wire-уровне (ADR-4). Гейт `buf breaking` в CI не даёт КОММИТНУТЬ ломающее
// изменение; эти тесты доказывают РАНТАЙМ-свойство, на которое мы полагаемся
// из-за самообновления: в проде одновременно работают агенты и сервер разных
// версий, и стороны обязаны переживать поля/значения, которых не знают.
//
// «Старую» версию контракта эмулируем напрямую байтами (protowire): добавляем в
// сообщение поле/enum-значение из гипотетического будущего контракта и
// убеждаемся, что текущий генерированный код это переживает.
package contract

import (
	"bytes"
	"testing"

	pb "github.com/Floodww/RoutineOps/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// TestNewFieldIgnoredByOldPeer: новый сервер шлёт сообщение с полем, добавленным
// в будущем контракте; старый агент (текущий код) декодирует без ошибки,
// сохраняет известные поля и НЕ теряет неизвестное при ре-сериализации.
func TestNewFieldIgnoredByOldPeer(t *testing.T) {
	task := &pb.Task{
		TaskId:        "t-1",
		ScriptContent: "echo hi",
		Platform:      "macOS",
		Priority:      pb.TaskPriority_TASK_PRIORITY_HIGH,
	}
	raw, err := proto.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}

	// Поле №99 (varint) — его нет в текущем Task, имитируем будущее расширение.
	future := protowire.AppendTag(nil, 99, protowire.VarintType)
	future = protowire.AppendVarint(future, 42)
	raw = append(raw, future...)

	var got pb.Task
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("старый агент упал на неизвестном поле (graceful degrade сломан): %v", err)
	}
	if got.TaskId != "t-1" || got.Platform != "macOS" || got.Priority != pb.TaskPriority_TASK_PRIORITY_HIGH {
		t.Fatalf("известные поля повреждены: %+v", &got)
	}
	if len(got.ProtoReflect().GetUnknown()) == 0 {
		t.Fatal("неизвестное поле должно сохраняться (важно для ретрансляции/аудита)")
	}

	// Ре-сериализация не теряет неизвестное поле.
	re, err := proto.Marshal(&got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(re, future) {
		t.Fatal("неизвестное поле потеряно при ре-сериализации")
	}
}

// TestUnknownEnumValueTolerated: будущая версия добавляет новое значение enum
// (новый TaskPriority/AlertType/TaskStatus). proto3 — открытые enum: старый код
// должен декодировать неизвестное значение как его число, без ошибки и паники.
func TestUnknownEnumValueTolerated(t *testing.T) {
	// Task.priority (поле 5) = 99 — значения нет в текущем TaskPriority.
	raw := protowire.AppendTag(nil, 1, protowire.BytesType)
	raw = protowire.AppendString(raw, "t-2")
	raw = protowire.AppendTag(raw, 5, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 99)

	var got pb.Task
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("неизвестное значение enum уронило декодер: %v", err)
	}
	if int32(got.Priority) != 99 {
		t.Fatalf("открытый enum: ждали сырое значение 99, получили %d", got.Priority)
	}
	if got.TaskId != "t-2" {
		t.Fatalf("известное поле повреждено: %q", got.TaskId)
	}
}

// TestMissingFieldDecodesToZero: старый агент шлёт сообщение БЕЗ поля, которое
// новый сервер уже знает (поле добавлено позже). Новый сервер (текущий код)
// получает нулевое значение, без ошибки — обратная совместимость.
func TestMissingFieldDecodesToZero(t *testing.T) {
	// Эмулируем «старый» SecurityEvent только с частью полей (без occurred_at=4
	// и admin_access_request_id=5).
	raw := protowire.AppendTag(nil, 2, protowire.VarintType) // alert_type = FORBIDDEN_SOFTWARE
	raw = protowire.AppendVarint(raw, uint64(pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE))
	raw = protowire.AppendTag(raw, 3, protowire.BytesType) // details
	raw = protowire.AppendString(raw, "torrent.exe")

	var got pb.SecurityEvent
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("новый сервер не смог разобрать урезанное сообщение: %v", err)
	}
	if got.AlertType != pb.AlertType_ALERT_TYPE_FORBIDDEN_SOFTWARE || got.Details != "torrent.exe" {
		t.Fatalf("известные поля повреждены: %+v", &got)
	}
	if got.OccurredAt != 0 || got.AdminAccessRequestId != "" {
		t.Fatalf("отсутствующие поля должны быть нулевыми: occurred_at=%d req_id=%q", got.OccurredAt, got.AdminAccessRequestId)
	}
}
