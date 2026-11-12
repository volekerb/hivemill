package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pySource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "bin", "factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pyTuple returns the quoted names of `NAME = (...)` or `NAME = {...}` in the Python source.
func pyTuple(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)\n` + name + ` *= *[({](.*?)[)}]\n`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s not found in bin/factory_cli.py", name)
	}
	return regexp.MustCompile(`"([^"]+)"`).FindAllString(m[1], -1)
}

func unquoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.Trim(s, `"`)
	}
	return out
}

func TestOwnerCapKeysMatchPython(t *testing.T) {
	got := unquoteAll(pyTuple(t, pySource(t), "OWNER_CAP_KEYS"))
	if !reflect.DeepEqual(got, sbOwnerCapKeys) {
		t.Fatalf("sbOwnerCapKeys %v != Python OWNER_CAP_KEYS %v; update scoreboard.go", sbOwnerCapKeys, got)
	}
}

func TestWorkerRolesMatchPython(t *testing.T) {
	got := unquoteAll(pyTuple(t, pySource(t), "WORKER_ROLES"))
	want := append([]string(nil), sbWorkerRoles...)
	// Python's set is unordered; compare as sets.
	m := map[string]bool{}
	for _, r := range got {
		m[r] = true
	}
	if len(m) != len(want) {
		t.Fatalf("sbWorkerRoles %v != Python WORKER_ROLES %v", want, got)
	}
	for _, r := range want {
		if !m[r] {
			t.Fatalf("sbWorkerRoles has %q, Python WORKER_ROLES %v does not", r, got)
		}
	}
}

func TestDirectorRow(t *testing.T) {
	home := t.TempDir()
	path, err := buildGoldenDB(t, home, "scorecard")
	if err != nil {
		t.Fatal(err)
	}
	db, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	end := time.Date(2026, 10, 16, 23, 0, 0, 0, time.FixedZone("", 0))
	row, err := sbDirectorRow(db, end.Add(-24*time.Hour), end, 24, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// samples 09:00 (w 2.5 min) and 09:02:30 (last, 1 min): starved 2.5 min rounds half to even -> 2; share 1/3 and 2/3.
	want := DirectorRow{Hours: 24, DayStart: 8, DayEnd: 20, Samples: 2, Target: 3, MedianQ: "1.5", StarvedMin: 2,
		Longest: 2, LongestGoal: "G-1", Share: 43, Scores: []int{1, 2}}
	want.AvgWorkers = (2.5*1 + 1.0*2) / 3.5
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("director row\n got %+v\nwant %+v", row, want)
	}
	if got, want := sbDirectorLine(row), "director (24h, daytime 08-20): avg workers 1.3 · share 43% · target 3 · "+
		"median workers with queue 1.5 · P1 starved 2 min (longest 2 min G-1) · cap breaches 0 · boss scores 1, 2 · TWO LOW SCORES"; got != want {
		t.Fatalf("line\n got %q\nwant %q", got, want)
	}
}

func TestDirectorLineNoSamples(t *testing.T) {
	got := sbDirectorLine(DirectorRow{Hours: 48, DayStart: 8, DayEnd: 20, Breaches: 2, Scores: []int{3, 1}})
	want := "director (48h, daytime 08-20): no utilization samples · cap breaches 2 · boss scores 3, 1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseTS(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	for _, c := range []struct {
		in   string
		ok   bool
		want string // UTC instant
	}{
		{"2026-10-10T09:00:00+02:00", true, "2026-10-10T07:00:00Z"},
		{"2026-10-10T09:00:00Z", true, "2026-10-10T09:00:00Z"},
		{"2026-10-10T09:00:00", true, "2026-10-10T07:00:00Z"}, // naive = local (Berlin, CEST)
		{"2026-10-10 09:00:00.5", true, "2026-10-10T07:00:00Z"},
		{"2026-10-10", true, "2026-10-09T22:00:00Z"},
		{"2026-02-30T00:00:00", false, ""},
		{"garbage", false, ""},
		{"", false, ""},
		{"0000-01-01T00:00:00+00:00", false, ""}, // Python: year 0 is out of range
		{"0000-01-01", false, ""},
	} {
		got, ok, err := sbParseTS(c.in, berlin)
		if err != nil || ok != c.ok {
			t.Fatalf("%q: ok=%v err=%v, want ok=%v", c.in, ok, err, c.ok)
		}
		if ok && got.UTC().Format("2006-01-02T15:04:05Z") != c.want {
			t.Fatalf("%q: got %s want %s", c.in, got.UTC().Format(time.RFC3339), c.want)
		}
	}
	if _, _, err := sbParseTS("2026-10-10T09:00:00 +02:00", time.UTC); err == nil {
		t.Fatal("an unknown date-shaped value must delegate, not be ignored")
	}
}

func TestPyIntStr(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int64
		ok   bool
		fail bool
	}{
		{"3", 3, true, false}, {" +3 ", 3, true, false}, {"-2", -2, true, false}, {"1_0", 10, true, false},
		{"x", 0, false, false}, {"", 0, false, false}, {"1__0", 0, false, false}, {"_1", 0, false, false},
		{"3.0", 0, false, false}, {"٣", 0, false, true},
	} {
		n, ok, err := pyIntStr(c.in)
		if (err != nil) != c.fail || ok != c.ok || n != c.n {
			t.Fatalf("%q: got (%d,%v,%v), want (%d,%v,fail=%v)", c.in, n, ok, err, c.n, c.ok, c.fail)
		}
	}
}

func FuzzSbParseTS(f *testing.F) {
	for _, s := range []string{"2026-10-10T09:00:00+02:00", "0000-01-01", "9999-12-31T23:59:59Z", "2026-10-10T09:00:00.5", "2026-10-10T09:00:00+01:00:30", "2026-02-30", "x", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, ok, err := sbParseTS(s, time.UTC)
		if err != nil || !ok {
			return
		}
		if y := got.Year(); y < 1 || y > 9999 {
			t.Fatalf("%q: year %d outside 1..9999", s, y)
		}
		iso := sbISO(got)
		back, ok2, err2 := sbParseTS(iso, time.UTC)
		// sbISO drops fractional seconds and offset seconds, so compare at its precision.
		if err2 != nil || !ok2 || sbISO(back) != iso {
			t.Fatalf("%q: sbISO %q does not round-trip (ok=%v err=%v)", s, iso, ok2, err2)
		}
	})
}

func FuzzPyIntStr(f *testing.F) {
	for _, s := range []string{"0", "-5", " +1_000 ", "9223372036854775807", "9223372036854775808", "1__0", "_1", "é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok, err := pyIntStr(s)
		if err != nil || !ok {
			return
		}
		if want, perr := strconv.ParseInt(strconv.FormatInt(n, 10), 10, 64); perr != nil || want != n {
			t.Fatalf("%q: %d does not round-trip", s, n)
		}
		if v, perr := strconv.ParseInt(strings.TrimSpace(s), 10, 64); perr == nil && v != n {
			t.Fatalf("%q: pyIntStr=%d, ParseInt=%d", s, n, v)
		}
	})
}
