package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeBrowser reads NUL-framed requests from r and answers each through reply.
func fakeBrowser(t *testing.T, r io.Reader, w io.Writer, reply func(req map[string]any) []byte) {
	t.Helper()
	go func() {
		br := bufio.NewReader(r)
		for {
			raw, err := br.ReadBytes(0)
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(raw[:len(raw)-1], &req); err != nil {
				t.Errorf("fake browser got bad JSON %q: %v", raw, err)
				return
			}
			if out := reply(req); out != nil {
				w.Write(append(out, 0))
			}
		}
	}()
}

func TestCDPFramingIDMatchingAndErrors(t *testing.T) {
	toBrowserR, toBrowserW := io.Pipe()
	fromBrowserR, fromBrowserW := io.Pipe()
	var seenSession string
	fakeBrowser(t, toBrowserR, fromBrowserW, func(req map[string]any) []byte {
		id := int(req["id"].(float64))
		switch req["method"] {
		case "Test.ok":
			seenSession, _ = req["sessionId"].(string)
			b, _ := json.Marshal(map[string]any{"id": id, "result": map[string]any{"echo": req["params"]}})
			return b
		case "Test.fail":
			b, _ := json.Marshal(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "boom"}})
			return b
		}
		return nil // Test.silent: never answered
	})
	c := newCDP(toBrowserW, fromBrowserR, 200*time.Millisecond)
	ctx := context.Background()

	got, err := c.call(ctx, "sess-1", "Test.ok", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("ok call: %v", err)
	}
	if !strings.Contains(string(got), `"a":"b"`) || seenSession != "sess-1" {
		t.Fatalf("reply %s, session seen by the browser %q", got, seenSession)
	}
	if _, err := c.call(ctx, "", "Test.fail", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error reply must become an error naming the message, got %v", err)
	}
	if _, err := c.call(ctx, "", "Test.silent", nil); err == nil || !strings.Contains(err.Error(), "no reply") {
		t.Fatalf("a silent browser must time the call out, got %v", err)
	}
	fromBrowserW.Close()
	time.Sleep(50 * time.Millisecond)
	if _, err := c.call(ctx, "", "Test.ok", nil); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("a closed pipe must fail calls, got %v", err)
	}
}

func TestCDPConcurrentCallsGetTheirOwnReply(t *testing.T) {
	toBrowserR, toBrowserW := io.Pipe()
	fromBrowserR, fromBrowserW := io.Pipe()
	fakeBrowser(t, toBrowserR, fromBrowserW, func(req map[string]any) []byte {
		b, _ := json.Marshal(map[string]any{"id": int(req["id"].(float64)), "result": map[string]any{"m": req["method"]}})
		return b
	})
	c := newCDP(toBrowserW, fromBrowserR, time.Second)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		m := "M" + string(rune('a'+i))
		go func() {
			raw, err := c.call(context.Background(), "", m, nil)
			if err == nil && !strings.Contains(string(raw), `"`+m+`"`) {
				err = io.ErrUnexpectedEOF
			}
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
