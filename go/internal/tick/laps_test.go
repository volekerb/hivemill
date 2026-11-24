package tick

import (
	"encoding/json"
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLaps(t *testing.T) {
	// k>0: child ran 10..20 (E-S=10), laps 2+3, tail 1: unaccounted = 10-5-1 = 4, gap before = 10-7 = 3
	tm := Timer{Last: 7}
	tm.Child(10, 20, []Lap{{"a", 2}, {"b", 3}}, 1)
	if len(tm.Laps) != 2 || !near(tm.Laps[0].Sec, 2+3+4) || !near(tm.Laps[1].Sec, 3) || !near(tm.Last, 19) {
		t.Fatalf("k>0: %+v last %v", tm.Laps, tm.Last)
	}
	// k=0: nothing appended, last stays: the child's time flows into the next lap
	tm = Timer{Last: 7}
	tm.Child(10, 20, nil, 0)
	if len(tm.Laps) != 0 || tm.Last != 7 {
		t.Fatalf("k=0: %+v last %v", tm.Laps, tm.Last)
	}
	tm.Lap("go_unit", 25) // a Go lap after a lapless Python unit: 25-7 = 18
	if !near(tm.Laps[0].Sec, 18) || tm.Last != 25 {
		t.Fatalf("lapless before a Go lap: %+v", tm.Laps)
	}
	// the laps plus the tail of the last child cover the run exactly
	tm = Timer{Last: 0}
	tm.Child(1, 11, []Lap{{"a", 4}, {"b", 4}}, 1)
	total := 0.0
	for _, l := range tm.Laps {
		total += l.Sec
	}
	if !near(total+1, 11) {
		t.Fatalf("laps %+v do not cover 0..11 less the tail", tm.Laps)
	}
}

func TestCountersMergeKeepsOrder(t *testing.T) {
	var a, b Counters
	if err := json.Unmarshal([]byte(`{"x":1,"y":0.5,"z":2}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"w":7,"y":0.25,"x":2}`), &b); err != nil {
		t.Fatal(err)
	}
	a.Merge(b)
	out, _ := json.Marshal(a)
	if string(out) != `{"x":3,"y":0.75,"z":2,"w":7}` {
		t.Fatalf("%s", out)
	}
	var f Counters
	f.Add("s", "0.0")
	f.Add("s", "0.0")
	if o, _ := json.Marshal(f); string(o) != `{"s":0.0}` {
		t.Fatalf("a float zero must stay a float: %s", o)
	}
}

// TestNewLapsDropsTheHandedInLaps: the real Python child writes the handoff laps back with its own after them; a fake
// child that returns only its own laps is taken as is. Expected shapes are what bin/factory writes (_tick_handoff_write).
func TestNewLapsDropsTheHandedInLaps(t *testing.T) {
	in := []Lap{{"migrate_db", 0.5}, {"digest", 0.25}}
	for _, c := range []struct {
		name string
		ret  []Lap
		want []Lap
	}{
		{"cumulative", []Lap{{"migrate_db", 0.5}, {"digest", 0.25}, {"scout", 1}}, []Lap{{"scout", 1}}},
		{"cumulative_nothing_new", []Lap{{"migrate_db", 0.5}, {"digest", 0.25}}, []Lap{}},
		{"only_new", []Lap{{"scout", 1}}, []Lap{{"scout", 1}}},
		{"only_new_longer_than_input", []Lap{{"scout", 1}, {"retro", 2}, {"skilleval", 3}}, []Lap{{"scout", 1}, {"retro", 2}, {"skilleval", 3}}},
		{"changed_prefix", []Lap{{"migrate_db", 0.5}, {"digest", 0.3}, {"scout", 1}}, []Lap{{"migrate_db", 0.5}, {"digest", 0.3}, {"scout", 1}}},
	} {
		got := newLaps(in, c.ret)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v want %v", c.name, got, c.want)
			}
		}
	}
	if got := newLaps(nil, []Lap{{"a", 1}}); len(got) != 1 {
		t.Errorf("no input laps: %v", got)
	}
}
