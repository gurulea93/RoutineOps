//go:build screen

package screen

import (
	"bytes"
	"sync"
	"testing"
)

func TestCtlWriterConcurrentMessagesStayWhole(t *testing.T) {
	var buf bytes.Buffer // под мьютексом ctlWriter
	c := &ctlWriter{w: &buf}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.send(msgStop, bytes.Repeat([]byte{byte(i)}, 1000+i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	r := bytes.NewReader(buf.Bytes())
	for r.Len() > 0 {
		typ, body, err := readMsg(r)
		if err != nil || typ != msgStop || len(body) < 1000 {
			t.Fatalf("message %d: type %d, %d B, %v", len(seen), typ, len(body), err)
		}
		i := len(body) - 1000
		if !bytes.Equal(body, bytes.Repeat([]byte{byte(i)}, 1000+i)) {
			t.Fatalf("message from writer %d is mixed with another", i)
		}
		seen[i] = true
	}
	if len(seen) != n {
		t.Fatalf("decoded %d distinct messages, want %d", len(seen), n)
	}
}

func TestCtlWriterAskKeyframe(t *testing.T) {
	var buf bytes.Buffer
	newLatestFrame((&ctlWriter{w: &buf}).askKeyframe).askKey()
	if typ, body, err := readMsg(&buf); err != nil || typ != msgKeyframe || len(body) != 0 {
		t.Fatalf("type %d body %d err %v", typ, len(body), err)
	}
}
