package testcount

import (
	"strings"
	"testing"
)

// FuzzDecide: the gate may pass a drop only when the body has a tests-removed: line with a non-blank reason, and never
// panics (G7). Run: go test ./internal/citools/testcount -run XXX -fuzz FuzzDecide
func FuzzDecide(f *testing.F) {
	for _, s := range []string{"", "tests-removed: x", "tests-removed:\u00a0", "  TESTS-REMOVED:\t\u3000y", "tests-removed:\n", "a\r\ntests-removed: r"} {
		f.Add(s, 10, 9)
	}
	f.Fuzz(func(t *testing.T, body string, base, pr int) {
		ok, msg := Decide(base, pr, body)
		if msg == "" {
			t.Fatal("empty message")
		}
		if pr >= base {
			if !ok {
				t.Fatalf("pr %d >= base %d must pass", pr, base)
			}
			return
		}
		if ok && !strings.Contains(strings.ToLower(body), "tests-removed:") {
			t.Fatalf("a drop passed without a bypass line: %q", body)
		}
		if ok {
			reason := msg[strings.LastIndex(msg, "tests-removed: ")+len("tests-removed: "):]
			if strings.TrimFunc(reason, func(r rune) bool { return r == ' ' || r == '\t' || r == '\u00a0' }) == "" {
				t.Fatalf("blank reason accepted: %q", body)
			}
		}
	})
}
