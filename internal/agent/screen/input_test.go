//go:build screen

package screen

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

type fakeInjector struct {
	mu       sync.Mutex
	batches  []*pb.SessionInput
	closed   bool
	released int
}

func (f *fakeInjector) Apply(in *pb.SessionInput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, in)
}
func (f *fakeInjector) ReleaseAll() { f.mu.Lock(); f.released++; f.mu.Unlock() }
func (f *fakeInjector) Close()      { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func (f *fakeInjector) snapshot() (n int, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches), f.closed
}

func inputMsg(events ...*pb.InputEvent) *pb.SessionDown {
	return &pb.SessionDown{Msg: &pb.SessionDown_Input{Input: &pb.SessionInput{
		FrameWidth: 1280, FrameHeight: 720, Events: events,
	}}}
}

func TestLoopDownForwardsInputWhenControlGranted(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1", AllowInput: true})
	fake := &fakeScreenStream{}
	fake.push(inputMsg(&pb.InputEvent{Kind: "mouse_down", X: 10, Y: 20}))
	var ctlBuf bytes.Buffer
	runLoopDownLog(t, s, fake, &ctlBuf)

	typ, body, err := readMsg(&ctlBuf)
	if err != nil || typ != msgInput {
		t.Fatalf("worker got type %d err %v", typ, err)
	}
	var got pb.SessionInput
	if err := proto.Unmarshal(body, &got); err != nil || got.GetFrameWidth() != 1280 ||
		len(got.GetEvents()) != 1 || got.GetEvents()[0].GetKind() != "mouse_down" {
		t.Fatalf("payload %+v err %v", &got, err)
	}
}

func TestLoopDownReturnControlReleasesAndStopsForwarding(t *testing.T) {
	s := newSession(context.Background(), &pb.ScreenSessionCommand{SessionId: "s1", AllowInput: true})
	fake := &fakeScreenStream{}
	fake.push(narrowMsg([]int32{0}, false)) // возврат управления: допустимое сужение
	fake.push(inputMsg(&pb.InputEvent{Kind: "key_down", Code: "KeyA"}))
	var ctlBuf bytes.Buffer
	runLoopDownLog(t, s, fake, &ctlBuf)

	typ, body, err := readMsg(&ctlBuf)
	if err != nil || typ != msgInput {
		t.Fatalf("release not sent: type %d err %v", typ, err)
	}
	var rel pb.SessionInput
	if err := proto.Unmarshal(body, &rel); err != nil || !rel.GetReleaseAll() {
		t.Fatalf("release payload %+v err %v", &rel, err)
	}
	if ctlBuf.Len() != 0 {
		t.Fatal("input after the return of control must not reach the worker")
	}
	if s.inputOn.Load() {
		t.Fatal("control must stay returned")
	}
}

func TestWorkerAllowInputWithoutInjectorRefuses(t *testing.T) {
	var out bytes.Buffer
	got := RunWorker(context.Background(), WorkerConfig{
		AllowInput:  true,
		NewCapturer: func() (Capturer, error) { return newFakeScreen(64, 64), nil },
		ShowNotice:  func(_, _ string) (Notice, error) { return &fakeNotice{}, nil },
	}, bytes.NewReader(nil), &out)
	if got != ReasonInputUnavailable {
		t.Fatalf("reason %s", got)
	}
	if o := parseWorkerOut(t, out.Bytes()); o.hello != nil || len(o.frames) != 0 {
		t.Fatalf("no frames before the refusal: %+v", o)
	}
}

func TestWorkerInjectorFailureRefuses(t *testing.T) {
	var out bytes.Buffer
	got := RunWorker(context.Background(), WorkerConfig{
		AllowInput:  true,
		NewInjector: func(int, int) (Injector, error) { return nil, errors.New("no desktop") },
		NewCapturer: func() (Capturer, error) { return newFakeScreen(64, 64), nil },
		ShowNotice:  func(_, _ string) (Notice, error) { return &fakeNotice{}, nil },
	}, bytes.NewReader(nil), &out)
	if got != ReasonInputUnavailable {
		t.Fatalf("reason %s", got)
	}
}

func TestWorkerAppliesInputAndReleasesOnEnd(t *testing.T) {
	inj := &fakeInjector{}
	inR, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan EndReason, 1)
	go func() {
		done <- RunWorker(context.Background(), WorkerConfig{
			AllowInput:  true,
			NewInjector: func(int, int) (Injector, error) { return inj, nil },
			NewCapturer: func() (Capturer, error) { return newFakeScreen(64, 64), nil },
			ShowNotice:  func(_, _ string) (Notice, error) { return &fakeNotice{}, nil },
		}, inR, &out)
	}()
	b, err := proto.Marshal(&pb.SessionInput{FrameWidth: 64, FrameHeight: 64,
		Events: []*pb.InputEvent{{Kind: "mouse_move", X: 1, Y: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMsg(inW, msgInput, b); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n, _ := inj.snapshot(); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input was not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writeJSONMsg(inW, msgStop, wireStop{Reason: "VIEWER_GONE"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	if _, closed := inj.snapshot(); !closed {
		t.Fatal("injector must be closed (and release everything) when the session ends")
	}
}

func TestWorkerIgnoresInputWithoutControl(t *testing.T) {
	inR, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan EndReason, 1)
	go func() {
		done <- RunWorker(context.Background(), WorkerConfig{
			NewInjector: func(int, int) (Injector, error) {
				t.Error("injector must not be created without allow_input")
				return nil, errors.New("unexpected")
			},
			NewCapturer: func() (Capturer, error) { return newFakeScreen(64, 64), nil },
			ShowNotice:  func(_, _ string) (Notice, error) { return &fakeNotice{}, nil },
		}, inR, &out)
	}()
	b, _ := proto.Marshal(&pb.SessionInput{Events: []*pb.InputEvent{{Kind: "key_down", Code: "KeyA"}}})
	_ = writeMsg(inW, msgInput, b)
	_ = writeJSONMsg(inW, msgStop, wireStop{Reason: "VIEWER_GONE"})
	select {
	case got := <-done:
		if got != "VIEWER_GONE" {
			t.Fatalf("reason %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
}
