package tokens

import (
	"fmt"
	"time"
)

// Now is the clock the attribution functions use for "now" (Python dt.datetime.now().astimezone()); tests replace it.
var Now = time.Now

// zone is the zone of Python's astimezone() / naive timestamps: the machine's. Tests replace it.
var zone = time.Now().Location()

var tsLayouts = []string{
	"2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05-0700",
	"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04Z07:00",
}
var tsNaiveLayouts = []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"}

// parseTS is _parse_ts: an ISO timestamp (UTC "Z" in logs, local offset in the DB); a naive value counts as local time.
// ok=false is Python's None (empty, not a string, or not ISO).
func parseTS(s string) (time.Time, bool) {
	for _, l := range tsLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	for _, l := range tsNaiveLayouts {
		if t, err := time.ParseInLocation(l, s, zone); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// localDay is _local_day: the local calendar day of ts, "unknown" when ts does not parse.
func localDay(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return "unknown"
	}
	return t.In(zone).Format("2006-01-02")
}

// isoformat is datetime.isoformat() of an aware value: microseconds only when non-zero, offset as +hh:mm.
func isoformat(t time.Time) string {
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s + t.Format("-07:00")
}

// isoSeconds is isoformat(timespec="seconds").
func isoSeconds(t time.Time) string { return t.Format("2006-01-02T15:04:05-07:00") }
