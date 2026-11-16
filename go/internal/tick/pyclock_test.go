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

func FuzzPyFromISO(f *testing.F) {
	for _, s := range []string{
		"2026-10-31T12:00:00+00:00",
		"2026-10-31T12:00:00",
		"2026-10-31",
		"1677-01-01",
		"1677-01-01T00:00:00Z",
		"2262-04-11T23:47:17Z",
		"9999-01-01",
		"9999-01-01T00:00:00Z",
		"bad",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tm, ok := pyFromISO(s, time.UTC)
		if !ok {
			return
		}
		got := epochSeconds(tm)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Fatalf("non-finite epoch %v for %q", got, s)
		}
		// UnixNano is defined only inside this span. float64(unixNano)/1e9 rounds once the nanosecond count
		// exceeds 2^53, so the two formulas agree within a microsecond, not bit for bit.
		if y := tm.Year(); y >= 1680 && y <= 2260 {
			want := float64(tm.UnixNano()) / 1e9
			if math.Abs(got-want) > 1e-6 {
				t.Fatalf("%q: epoch %v, UnixNano formula %v", s, got, want)
			}
		}
	})
}

func FuzzPyJSONFloat(f *testing.F) {
	for _, s := range []string{
		`{"at": 1}`,
		`{"at": 1.5}`,
		`{"at": "1_000"}`,
		`{"at": " 12 "}`,
		`{"at": NaN}`,
		`{"at": Infinity}`,
		`{"at": 1e400}`,
		`not json`,
		``,
		`{"at": "١٢"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		pyParse(s)
		got, ok := pyFloatAt(s)
		if ok && (math.IsNaN(got) || math.IsInf(got, 0)) {
			t.Fatalf("pyFloatAt returned a non-finite %v", got)
		}
		pyFloatStr(s)
	})
}
