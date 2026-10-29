package admin

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Заявка из трея сериализуется в файл и читается службой обратно.
func TestUserRequestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-request.json")
	if err := WriteUserRequest(path, "нужны права для установки ПО"); err != nil {
		t.Fatal(err)
	}
	r, err := ReadUserRequest(path)
	if err != nil {
		t.Fatalf("ReadUserRequest: %v", err)
	}
	if r.Reason != "нужны права для установки ПО" || r.RequestedAt == 0 {
		t.Fatalf("заявка прочитана неверно: %+v", r)
	}
}

// Нет файла-заявки → os.ErrNotExist (служба трактует как «заявок нет»).
func TestReadUserRequestMissing(t *testing.T) {
	_, err := ReadUserRequest(filepath.Join(t.TempDir(), "нет.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ожидали ErrNotExist, got %v", err)
	}
}

func TestIsTerminalRequestErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"FailedPrecondition (no owner)", status.Error(codes.FailedPrecondition, "device has no owner user assigned"), true},
		{"NotFound", status.Error(codes.NotFound, "device not found"), true},
		{"InvalidArgument", status.Error(codes.InvalidArgument, "bad"), true},
		{"Unavailable (transient)", status.Error(codes.Unavailable, "down"), false},
		{"DeadlineExceeded (transient)", status.Error(codes.DeadlineExceeded, "slow"), false},
		{"plain error (dial fail → Unknown)", io.EOF, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTerminalRequestErr(tt.err); got != tt.want {
				t.Errorf("isTerminalRequestErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// processRequest: успех и терминальный отказ снимают файл-заявку (конец спама),
// транзиент — оставляют для повтора.
func TestProcessRequest(t *testing.T) {
	tests := []struct {
		name      string
		sendErr   error
		wantSent  bool
		fileStays bool
	}{
		{"success → файл снят", nil, true, false},
		{"терминальный (no owner) → файл снят, спама нет", status.Error(codes.FailedPrecondition, "no owner"), true, false},
		{"транзиент → файл оставлен для повтора", status.Error(codes.Unavailable, "server down"), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			path := filepath.Join(t.TempDir(), "admin-request.json")
			if err := WriteUserRequest(path, "нужны права"); err != nil {
				t.Fatalf("WriteUserRequest: %v", err)
			}
			var sent bool
			send := func(reason string) error {
				sent = true
				if reason != "нужны права" {
					t.Errorf("send получил reason=%q", reason)
				}
				return tt.sendErr
			}

			// Act
			processRequest(path, send, discardLog())

			// Assert
			if sent != tt.wantSent {
				t.Errorf("send вызван=%v, want %v", sent, tt.wantSent)
			}
			_, statErr := ReadUserRequest(path)
			fileStays := statErr == nil
			if fileStays != tt.fileStays {
				t.Errorf("файл остался=%v, want %v", fileStays, tt.fileStays)
			}
		})
	}
}

// Нет файла-заявки → send не вызывается (служба молча ждёт следующего тика).
func TestProcessRequestNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	var sent bool
	processRequest(path, func(string) error { sent = true; return nil }, discardLog())
	if sent {
		t.Error("send не должен вызываться при отсутствии файла-заявки")
	}
}
