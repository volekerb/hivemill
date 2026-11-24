package hm

import (
	"strings"
	"testing"
	"time"
)

func TestStopNote(t *testing.T) {
	r, _ := testRunner(t, "")
	var got []string
	r.Tools.Sh = func(_ string, _ time.Duration, _ []string, argv ...string) (int, string) {
		got = argv
		return 0, ""
	}
	r.StopNote("T-0001", "waiting for the owner on the tier")
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "factory task note T-0001 'waiting for the owner on the tier'") {
		t.Fatalf("note command %q", got)
	}
	got = nil
	r.StopNote("T-0001", "  ")
	if got != nil {
		t.Error("an empty note runs nothing")
	}
}
