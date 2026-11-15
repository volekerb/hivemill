package retro

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/scout"
)

func newDB(t testing.TB) *sql.DB {
	t.Helper()
	path := dbtest.New(t, t.TempDir(), factorydb.SchemaVersion)
	d, err := factorydb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ddl, err := os.ReadFile("testdata/retro_ddl.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

func mustExec(t testing.TB, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func fixture(t testing.TB) *sql.DB {
	db := newDB(t)
	mustExec(t, db, "INSERT INTO tasks VALUES('T-1','https://x/pr/1'),('T-2',NULL)")
	mustExec(t, db, `INSERT INTO rejection_incidents(cause,kind,task_id,verdict_id,created) VALUES
('test','review','T-1',5,'2026-11-14T09:00:00+01:00'),
('test','qa','T-2',NULL,'2026-11-14T10:00:00+01:00'),
('scope','review','T-1',7,'2026-11-14T11:30:00+01:00'),
('old','review','T-1',1,'2026-11-12T11:30:00+01:00')`)
	return db
}

func ts(t testing.TB, s string) time.Time {
	t.Helper()
	v, ok := scout.TS(s)
	if !ok {
		t.Fatalf("bad ts %s", s)
	}
	return v
}

// Every expected string below is the output of retro/report.py (python3 -I) on the same rows.
const wantPacket = `{
  "window_start": "2026-11-13T12:00:00+01:00",
  "window_end": "2026-11-14T12:00:00+01:00",
  "causes": [
    {
      "cause": "test",
      "count": 2
    },
    {
      "cause": "scope",
      "count": 1
    }
  ],
  "incidents": [
    {
      "cause": "test",
      "kind": "review",
      "task_id": "T-1",
      "pr_url": "https://x/pr/1",
      "verdict_id": 5
    },
    {
      "cause": "test",
      "kind": "qa",
      "task_id": "T-2",
      "pr_url": "",
      "verdict_id": null
    },
    {
      "cause": "scope",
      "kind": "review",
      "task_id": "T-1",
      "pr_url": "https://x/pr/1",
      "verdict_id": 7
    }
  ]
}`

const goodBody = `{"window_end": "2026-11-14T12:00:00+01:00", "causes": [{"cause": "test", "count": 2}, {"cause": "scope", "count": 1}],
 "evidence": [{"task_id": "T-1", "pr_url": "https://x/pr/1", "cause": "test", "z": 1, "a": "é"}],
 "fixes": [{"type": "check", "cause": "test", "text": "add a check é", "extra": 1}]}`

func TestPacketParity(t *testing.T) {
	db := fixture(t)
	p, err := BuildPacket(db, ts(t, "2026-11-14T12:00:00+01:00"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Text() != wantPacket {
		t.Fatalf("packet:\n%s", p.Text())
	}
}

func TestFileStoreShowDigestParity(t *testing.T) {
	db := fixture(t)
	body, err := DecodeOrdered([]byte(goodBody))
	if err != nil {
		t.Fatal(err)
	}
	day, norm, err := ValidateFile(db, body)
	if err != nil || day != "2026-11-14" {
		t.Fatalf("%s %v", day, err)
	}
	if err := StoreReport(db, day, norm, "retro-1", "2026-11-14T12:00:05+01:00"); err != nil {
		t.Fatal(err)
	}
	r, ok, err := LoadReport(db, day)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if r.Causes != `[{"cause": "test", "count": 2}, {"cause": "scope", "count": 1}]` ||
		r.Evidence != `[{"task_id": "T-1", "pr_url": "https://x/pr/1", "cause": "test", "z": 1, "a": "\u00e9"}]` ||
		r.Fixes != `[{"type": "check", "cause": "test", "text": "add a check \u00e9"}]` {
		t.Fatalf("stored %+v", r)
	}
	// the same day again replaces the row (upsert)
	if err := StoreReport(db, day, norm, "retro-2", "2026-11-14T12:00:09+01:00"); err != nil {
		t.Fatal(err)
	}
	r, _, _ = LoadReport(db, day)
	if r.Author != "retro-2" {
		t.Fatalf("%+v", r)
	}
	show, err := r.ShowText()
	if err != nil || !strings.Contains(show, `"day": "2026-11-14"`) || !strings.Contains(show, `"a": "\u00e9"`) {
		t.Fatalf("%s %v", show, err)
	}
	d, err := BuildDigestSection(db, ts(t, "2026-11-14T00:00:00+01:00"), ts(t, "2026-11-15T00:00:00+01:00"))
	want := "## Retro\n\n- test: 2\n- scope: 1\n- T-1 https://x/pr/1 (test)\n- check — add a check é\n"
	if err != nil || d != want {
		t.Fatalf("%q %v", d, err)
	}
	d, err = BuildDigestSection(db, ts(t, "2026-11-15T00:00:00+01:00"), ts(t, "2026-11-16T00:00:00+01:00"))
	if err != nil || d != "## Retro\n_nothing_\n" {
		t.Fatalf("%q %v", d, err)
	}
}

func validate(t *testing.T, db *sql.DB, js string) (string, error) {
	t.Helper()
	v, err := DecodeOrdered([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ValidateFile(db, v)
	if err == nil {
		return "", nil
	}
	if _, ok := err.(*ValueError); !ok {
		t.Fatalf("not a ValueError: %v", err)
	}
	return err.Error(), nil
}

func TestValidateRefusalsParity(t *testing.T) {
	db := fixture(t)
	good := `{"window_end": "2026-11-14T12:00:00+01:00", "causes": [{"cause": "test", "count": 2}, {"cause": "scope", "count": 1}],`
	cases := []struct{ in, want string }{
		{`[]`, "body must be a JSON object"},
		{`{}`, "window_end is required"},
		{`{"window_end": "x"}`, "window_end must be an ISO timestamp"},
		{`{"window_end": 5}`, "window_end must be an ISO timestamp"},
		{`{"window_end": "2026-11-14T12:00:00+01:00", "causes": {}}`, "causes must be a list"},
		{`{"window_end": "2026-11-14T12:00:00+01:00", "causes": [{"cause":"test","count":9}], "evidence": [], "fixes": []}`, "causes do not match the incidents in the window"},
		{good + `"evidence": [], "fixes": []}`, "fixes must have length 1 or 2 when causes are non-empty"},
		{good + `"evidence": [{"task_id": "T-2", "pr_url": "zz", "cause": "test"}], "fixes": [{"type":"check","cause":"test","text":"x"}]}`, "evidence[0] pr_url must match the task"},
		{good + `"evidence": [], "fixes": [{"type": "zz", "cause": "test", "text": "x"}]}`, "fixes[0] type must be one of ('check', 'template', 'prompt_rule')"},
		{good + `"evidence": [], "fixes": [{"type": "check", "cause": "test", "text": ""}]}`, "fixes[0] text must be 1-400 characters"},
		{good + `"evidence": [{"task_id": "T-9", "pr_url": "", "cause": "test"}], "fixes": [{"type":"check","cause":"test","text":"x"}]}`, "evidence[0] task T-9 is not an incident for test in the window"},
		{good + `"evidence": [{"task_id": "T-1", "cause": "test"}], "fixes": [{"type":"check","cause":"test","text":"x"}]}`, "evidence[0] missing pr_url"},
		{good + `"evidence": [], "fixes": [{"type":"check","cause":"nope","text":"x"}]}`, "fixes[0] cause not in the recomputed list"},
	}
	for _, c := range cases {
		got, _ := validate(t, db, c.in)
		if got != c.want {
			t.Errorf("%s\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// nine evidence items
	nine := strings.Repeat(`{"task_id":"T-1","pr_url":"https://x/pr/1","cause":"test"},`, 8) + `{"task_id":"T-1","pr_url":"https://x/pr/1","cause":"test"}`
	if got, _ := validate(t, db, good+`"evidence": [`+nine+`], "fixes": [{"type":"check","cause":"test","text":"x"}]}`); got != "at most 8 evidence items" {
		t.Errorf("got %q", got)
	}
	// an empty window needs empty evidence and fixes; with them it validates to the window's own day
	if got, _ := validate(t, db, `{"window_end": "2026-11-20T00:00:00+01:00", "causes": [], "evidence": [1], "fixes": []}`); got != "an empty window requires empty evidence and fixes" {
		t.Errorf("got %q", got)
	}
	if got, _ := validate(t, db, `{"window_end": "2026-11-20T00:00:00+01:00", "causes": [], "evidence": [], "fixes": []}`); got != "" {
		t.Errorf("empty window must validate, got %q", got)
	}
}

func TestFixTextBounds(t *testing.T) {
	db := fixture(t)
	mk := func(n int) string {
		return `{"window_end": "2026-11-14T12:00:00+01:00", "causes": [{"cause": "test", "count": 2}, {"cause": "scope", "count": 1}], "evidence": [],
 "fixes": [{"type":"check","cause":"test","text":"` + strings.Repeat("é", n) + `"}]}`
	}
	if got, _ := validate(t, db, mk(400)); got != "" { // 400 code points, 800 bytes: Python len() counts characters
		t.Errorf("400 chars must pass: %q", got)
	}
	if got, _ := validate(t, db, mk(401)); got != "fixes[0] text must be 1-400 characters" {
		t.Errorf("401 chars must fail: %q", got)
	}
}

// Python: int("٣") == 3, int(" 5_0 ") == 50, int("+7") == 7; NBSP and \v are whitespace to str.strip().
func TestUnicodeInt(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"٣", 3, true}, {" 5_0 ", 50, true}, {"+7", 7, true}, {" 5\x0b", 5, true}, {"०१", 1, true},
		{"5__0", 0, false}, {"_5", 0, false}, {"5_", 0, false}, {"", 0, false}, {"-", 0, false}, {"5.0", 0, false},
	} {
		got, ok := PyInt(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got (%d,%v) want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	if h, err := ParseHour(" ٦\x0b"); err != nil || h != 6 {
		t.Errorf("ParseHour: %d %v", h, err)
	}
	if _, err := ParseHour("24"); err == nil || err.Error() != "retro_hour must be an integer 0-23, got '24'; fix it with `factory config retro_hour 6`" {
		t.Errorf("%v", err)
	}
	if _, err := ParseMaxMinutes("0"); err == nil || err.Error() != "retro_max_minutes must be an integer >= 1, got '0'" {
		t.Errorf("%v", err)
	}
}

func TestDecodeOrdered(t *testing.T) {
	v, err := DecodeOrdered([]byte(`{"b":1,"a":[true,null,"x",1.5],"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := scout.Dumps(v, 0, false); got != `{"b": 2, "a": [true, null, "x", 1.5]}` { // last duplicate wins, first position kept
		t.Fatalf("%s", got)
	}
	for _, bad := range []string{``, `{`, `[1,]`, `{"a":1} x`, `{1:2}`} {
		if _, err := DecodeOrdered([]byte(bad)); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func FuzzDecodeOrdered(f *testing.F) {
	for _, s := range []string{goodBody, `[]`, `{"b":1,"b":2}`, `"x"`, `1e400`, `{"a":[{"b":null}]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := DecodeOrdered([]byte(s))
		if err == nil {
			scout.Dumps(v, 2, false)
		}
	})
}

func FuzzPyInt(f *testing.F) {
	for _, s := range []string{"5", "٣", " 5_0 ", "+7", "", "_", "99999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { PyInt(s) })
}

func FuzzValidateFile(f *testing.F) {
	db := fixture(f)
	for _, s := range []string{goodBody, `{}`, `[]`, `{"window_end":"2026-11-14T12:00:00+01:00","causes":[{"cause":1,"count":"2"}],"evidence":null,"fixes":null}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := DecodeOrdered([]byte(s))
		if err != nil {
			return
		}
		ValidateFile(db, v)
	})
}
