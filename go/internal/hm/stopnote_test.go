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
	if strings.Join(got, "|") != "factory|task|note|T-0001|waiting for the owner on the tier" {
		t.Fatalf("note command %q", got)
	}
	r.StopNote("T-0001", "it's $(x)")
	if got[4] != "it's $(x)" {
		t.Errorf("the note must reach the command as one argument: %q", got)
	}
	got = nil
	r.StopNote("T-0001", "  ")
	if got != nil {
		t.Error("an empty note runs nothing")
	}
}

func TestStopNoteFailureWarns(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { return 1, "no such task\n" }
	r.StopNote("T-0001", "why") // must not panic or return an error
}
