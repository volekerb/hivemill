package hm

import "testing"

func TestOwnTerminalsSkipsOtherCores(t *testing.T) {
	in := []Terminal{
		{Name: "planner", Address: "t1hw", State: "live"},
		{Name: "claude-coder-t-1-a7", Address: "box2~t1m5", State: "live"},
		{Name: "planner", Address: "box1/t1hw", State: "live"},
		{Name: "boss", Address: "box3/t16", State: "live"},
	}
	got := ownTerminals(in)
	if len(got) != 2 || got[0].Address != "t1hw" || got[1].Address != "box2~t1m5" {
		t.Fatalf("ownTerminals = %+v", got)
	}
	if alive(ownTerminals(in[2:]), true, "planner") {
		t.Fatal("another core's planner counted as this runner's")
	}
}
