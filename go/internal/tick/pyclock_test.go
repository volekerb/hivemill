package tick

import (
	"math"
	"strconv"
	"testing"
	"time"
)

type tableRow struct {
	Value string `json:"value"`
	Want  string `json:"want"`
}

func TestPyFloatAt(t *testing.T) {
	var rows []tableRow
	readJSON(t, "testdata/prune/floats.json", &rows)
	if len(rows) < 50 {
		t.Fatalf("table has %d rows", len(rows))
	}
	for _, r := range rows {
		f, ok := pyFloatAt(r.Value)
		if r.Want == "stale" {
			if ok {
				t.Errorf("%q: got %v, want stale", r.Value, f)
			}
			continue
		}
		w, err := strconv.ParseFloat(r.Want, 64)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || f != w {
			t.Errorf("%q: got %v ok=%v, want %v", r.Value, f, ok, w)
		}
	}
}

func TestPyClock(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata: ", err)
	}
	var rows []tableRow
	readJSON(t, "testdata/prune/clock.json", &rows)
	for _, r := range rows {
		if r.Value == "" { // FACTORY_NOW="" is "unset": the real clock
			continue
		}
		got, err := pyClockNow(func(string) string { return r.Value }, time.Now, berlin)
		if r.Want == "error" {
			if err == nil || err.Error() != "FACTORY_NOW must be an ISO timestamp, got "+r.Value {
				t.Errorf("%q: got %v %v, want the FactoryNowError text", r.Value, got, err)
			}
			continue
		}
		w, _ := strconv.ParseFloat(r.Want, 64)
		if err != nil || math.Abs(got-w) > 1e-6 {
			t.Errorf("%q: got %v %v, want %v", r.Value, got, err, w)
		}
	}
	// unset or empty: the real clock
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, v := range []string{""} {
		got, err := pyClockNow(func(string) string { return v }, func() time.Time { return fixed }, berlin)
		if err != nil || got != float64(fixed.Unix()) {
			t.Errorf("unset: %v %v", got, err)
		}
	}
}
