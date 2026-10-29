//go:build screen

package screen

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRunWorkerProcessRejectsNonConfigFirstMessage(t *testing.T) {
	var in, out bytes.Buffer
	_ = writeJSONMsg(&in, msgKeyframe, struct{}{})
	if code := runWorkerProcess(context.Background(), &in, &out, nil, nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
	typ, body, err := readMsg(&out)
	var e wireEnd
	if err != nil || typ != msgEnd || json.Unmarshal(body, &e) != nil || e.Reason != "PROTOCOL_ERROR" {
		t.Fatalf("got type %d %s err %v", typ, body, err)
	}
}

func TestRunWorkerProcessPassesConfig(t *testing.T) {
	var in, out bytes.Buffer
	_ = writeJSONMsg(&in, msgConfig, wireConfig{SessionID: "s1", Profile: "low", Operator: "op", Reason: "r", MaxDurationSec: 5})
	// Без захватчика и плашки RunWorker отвечает msgEnd с PLATFORM_UNSUPPORTED — это и
	// доказывает, что конфиг прочитан и RunWorker вызван.
	if code := runWorkerProcess(context.Background(), &in, &out, nil, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	typ, body, _ := readMsg(&out)
	var e wireEnd
	_ = json.Unmarshal(body, &e)
	if typ != msgEnd || e.Reason != "PLATFORM_UNSUPPORTED" {
		t.Fatalf("got type %d %s", typ, body)
	}
}
