package scout

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transcript struct {
	Gh map[string]struct {
		Args   []string
		Stdout *string
		Stderr string
		Exit   int
	} `json:"gh"`
	HN json.RawMessage `json:"hn"`
}

type fetchGolden struct {
	Runs []struct {
		Summary string
		Errors  []string
	} `json:"runs"`
	Candidates []map[string]any `json:"candidates"`
	Stars      []map[string]any `json:"stars"`
	Cursors    []map[string]any `json:"cursors"`
	HNURLs     []string         `json:"hn_urls"`
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// replayGh serves the recorded gh transcript: the process boundary. An unrecorded call is a gh failure, so a Go
// call the Python run never made shows up as a diff, never as network traffic.
func replayGh(t *testing.T, tr transcript) *Gh {
	return &Gh{Timeout: time.Second, Run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		rec, ok := tr.Gh[strings.Join(args, "\x1f")]
		if !ok {
			return nil, []byte("unrecorded call"), 1, nil
		}
		out := ""
		if rec.Stdout != nil {
			out = *rec.Stdout
		}
		return []byte(out), []byte(rec.Stderr), rec.Exit, nil
	}}
}

func rows(t *testing.T, db *sql.DB, q string) []map[string]any {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []map[string]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out
}

// normalizeRows turns DB values and decoded JSON into one comparable form (all numbers float64).
func normalizeRows(rows []map[string]any) []map[string]any {
	raw, _ := json.Marshal(rows)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// TestFetchGolden replays a synthetic GitHub (the recorded gh transcript) through Fetch three times and compares
// summaries, candidate rows, star snapshots, cursors and the Hacker News URLs with what scout/sources.py produced.
func TestFetchGolden(t *testing.T) {
	var tr transcript
	var g fetchGolden
	readJSON(t, "fetch_transcript.json", &tr)
	readJSON(t, "fetch_golden.json", &g)
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.RequestURI())
		w.Write(tr.HN)
	}))
	defer srv.Close()
	db := newDB(t)
	gh := replayGh(t, tr)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		gh.Calls, gh.SearchCalls = 0, 0
		run, err := Fetch(db, now.Add(time.Duration(i)*time.Hour), gh, srv.URL+"/search")
		if err != nil {
			t.Fatal(err)
		}
		if got := Summary(run); got != g.Runs[i].Summary {
			t.Errorf("run %d summary:\n got %s\nwant %s", i, got, g.Runs[i].Summary)
		}
		if !reflect.DeepEqual(run.Errors, g.Runs[i].Errors) {
			t.Errorf("run %d errors %v want %v", i, run.Errors, g.Runs[i].Errors)
		}
	}
	for table, q := range map[string]string{
		"candidates": "SELECT * FROM scout_candidates ORDER BY id",
		"stars":      "SELECT * FROM scout_stars ORDER BY candidate_id, ts",
		"cursors":    "SELECT * FROM scout_cursors ORDER BY source",
	} {
		want := map[string][]map[string]any{"candidates": g.Candidates, "stars": g.Stars, "cursors": g.Cursors}[table]
		got := normalizeRows(rows(t, db, q))
		if len(got) != len(want) {
			t.Errorf("%s: %d rows, want %d", table, len(got), len(want))
			continue
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("%s row %d:\n got %v\nwant %v", table, i, got[i], want[i])
			}
		}
	}
	if !reflect.DeepEqual(requested, g.HNURLs) {
		t.Errorf("HN requests:\n got %v\nwant %v", requested, g.HNURLs)
	}
}

func TestGhErrors(t *testing.T) {
	g := &Gh{Timeout: time.Second, Run: func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, []byte("boom\nsecond"), 2, nil
	}}
	if _, err := g.API(false, "repos/a/b", "--jq", ".x"); err == nil || err.Error() != "gh api repos/a/b: exit 2: boom" {
		t.Errorf("err %v", err)
	}
	g.Run = func(context.Context, []string) ([]byte, []byte, int, error) { return []byte{0xff, 0xfe}, nil, 0, nil }
	if _, err := g.API(false, "repos/a/b"); err == nil || !strings.Contains(err.Error(), "non-UTF-8 output") {
		t.Errorf("err %v", err)
	}
	g.Run = func(ctx context.Context, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, 0, ctx.Err()
	}
	g.Timeout = 20 * time.Millisecond
	if _, err := g.API(false, "repos/a/b"); err == nil || !strings.Contains(err.Error(), "timed out after 0 s") {
		t.Errorf("err %v", err)
	}
	g.Run = func(context.Context, []string) ([]byte, []byte, int, error) { return []byte("not json"), nil, 0, nil }
	if _, err := g.JSON(false, "repos/a/b"); err == nil || err.Error() != "gh api repos/a/b: response is not JSON" {
		t.Errorf("err %v", err)
	}
}

func TestSearchCallCap(t *testing.T) {
	g := &Gh{Run: func(context.Context, []string) ([]byte, []byte, int, error) { return []byte("{}"), nil, 0, nil }}
	for i := 0; i < MaxSearchCalls; i++ {
		if _, err := g.API(true, "search/repositories"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.API(true, "search/repositories"); err == nil || err.Error() != "search call cap (10) reached" {
		t.Errorf("err %v", err)
	}
}

func TestEntryTargetAndLinks(t *testing.T) {
	for _, c := range []struct {
		src        any
		repo, path string
		ok         bool
	}{
		{"./plugins/x", MarketplaceRepo, "plugins/x", true},
		{"../bad", "", "", false},
		{"./a/../b", "", "", false},
		{".", "", "", false},
		{"owner/repo", "", "", false},
		{map[string]any{"repo": "a/b", "path": "/p/"}, "a/b", "p", true},
		{map[string]any{"url": "https://github.com/a/b.git"}, "a/b", "", true},
		{map[string]any{"url": "https://gitlab.com/a/b"}, "", "", false},
	} {
		repo, path, ok := EntryTarget(c.src)
		if repo != c.repo || path != c.path || ok != c.ok {
			t.Errorf("EntryTarget(%v) = %q %q %v", c.src, repo, path, ok)
		}
	}
	text := "https://github.com/A/B.git https://github.com/sponsors/x https://github.com/own/list https://github.com/a/b. https://github.com/c/d"
	if got := ReadmeLinks(text, "own/list"); !reflect.DeepEqual(got, []string{"a/b", "c/d"}) {
		t.Errorf("links %v", got)
	}
}

func TestConsiderRefusesOddRepoAndPath(t *testing.T) {
	db := newDB(t)
	r := NewRun(db, &Gh{}, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), "")
	for _, c := range [][2]string{{"no slash", ""}, {"a/b", "../x"}, {"a/b", "x;y"}, {"a/b\n", ""}} {
		if _, err := r.Consider("search", c[0], c[1], nil, false, false); err == nil || !strings.Contains(err.Error(), "refusing odd repo/path") {
			t.Errorf("Consider(%q, %q) = %v", c[0], c[1], err)
		}
	}
}

func TestReadmeExcerpt(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes: the clip must not split a rune
	g := &Gh{Run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		if strings.Contains(args[0], "/contents/p/README.md") {
			return nil, []byte("404"), 1, nil
		}
		if strings.Contains(args[0], "/contents/p/SKILL.md") {
			return []byte(long), nil, 0, nil
		}
		return nil, []byte("404"), 1, nil
	}}
	text, e := ReadmeExcerpt(g, "a/b", "p", "abcdef0")
	if e != "" || len(text) != 4096 || strings.ContainsRune(text, '\ufffd') {
		t.Errorf("text len %d err %q", len(text), e)
	}
	if _, e := ReadmeExcerpt(g, "a/b", "", "abcdef0"); !strings.Contains(e, "exit 1") {
		t.Errorf("fallback err %q", e)
	}
	if _, e := ReadmeExcerpt(g, "bad", "", "zz"); e != "no repo or head sha recorded" {
		t.Errorf("err %q", e)
	}
}

func TestHNRefusesOtherSchemes(t *testing.T) {
	db := newDB(t)
	r := NewRun(db, &Gh{}, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), "ftp://example.com/x")
	if _, err := r.hnGet("q", 1); err == nil || !strings.Contains(err.Error(), "must be an http(s) or file URL") {
		t.Errorf("err %v", err)
	}
	if r.HNCalls != 0 {
		t.Error("counted a refused call")
	}
}
