package scout

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeKey(t *testing.T) {
	for in, want := range map[string]string{
		"gh:Foo/Bar.git":       "gh:foo/bar",
		"gh:Foo/Bar/Sub/Path/": "gh:foo/bar/Sub/Path",
		"GH: x/y":              "gh:x/y",
		"mkt:Off/Plug":         "mkt:off/plug",
	} {
		if got, err := NormalizeKey(in); err != nil || got != want {
			t.Errorf("NormalizeKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, msg := range map[string]string{
		"bad":          "bad candidate key 'bad': expected 'gh:<owner>/<repo>' or 'mkt:<marketplace>/<plugin>'",
		"gh:onlyowner": "bad candidate key 'gh:onlyowner': expected gh:<owner>/<repo>[/<path>]",
		"gh:/x":        "bad candidate key 'gh:/x': expected gh:<owner>/<repo>[/<path>]",
	} {
		_, err := NormalizeKey(in)
		var se *ScoutError
		if !errors.As(err, &se) || err.Error() != msg {
			t.Errorf("NormalizeKey(%q) err %v, want %q", in, err, msg)
		}
	}
}

func TestUpsertCandidate(t *testing.T) {
	db := newDB(t)
	id, created, err := UpsertCandidate(db, "gh:A/B.git/", "search", "2026-11-01T00:00:00+00:00", Facts{Kind: S("skill"), Stars: I(1), Licence: S("MIT")})
	if err != nil || !created || id != "C-0001" {
		t.Fatalf("%q %v %v", id, created, err)
	}
	id2, created, err := UpsertCandidate(db, "gh:a/b", "hn", "2026-11-02T00:00:00+00:00", Facts{Licence: S("Apache-2.0"), RiskDetail: S(JSONList([]string{"x"}))})
	if err != nil || created || id2 != id {
		t.Fatalf("%q %v %v", id2, created, err)
	}
	c, ok, err := GetCandidate(db, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if c.Key != "gh:a/b" || c.Sources != `["search", "hn"]` || c.SeenCount != 2 || c.LastSeen != "2026-11-02T00:00:00+00:00" ||
		c.FirstSeen != "2026-11-01T00:00:00+00:00" || c.Kind.String != "skill" || c.Licence.String != "Apache-2.0" || c.RiskDetail.String != `["x"]` || c.Status != "new" {
		t.Errorf("row %+v", c)
	}
	if _, _, err := UpsertCandidate(db, "gh:a/b", " ", "", Facts{}); err == nil || err.Error() != "source is required" {
		t.Errorf("empty source: %v", err)
	}
	id3, _, _ := UpsertCandidate(db, "mkt:x/y", "marketplace", "2026-11-01T00:00:00+00:00", Facts{})
	if id3 != "C-0002" {
		t.Errorf("next id %s", id3)
	}
}

// Rates are from scout/db.py snapshot_stars on the same snapshots.
func TestSnapshotStars(t *testing.T) {
	db := newDB(t)
	cid, _, _ := UpsertCandidate(db, "gh:a/b", "search", "2026-11-01T00:00:00+00:00", Facts{})
	steps := []struct {
		ts    string
		stars int64
		want  *float64
	}{
		{"2026-11-01T00:00:00+00:00", 10, nil},
		{"2026-11-04T12:00:00+00:00", 20, f(2.8571)},
		{"2026-11-20T00:00:00+00:00", 100, nil}, // the earlier snapshots are over 14 days old
		{"2026-11-21T00:00:00Z", 111, f(11.0)},
		{"2026-11-01 00:00:00", 7, f(11.0)}, // replaces the first snapshot, outside the window
	}
	for _, s := range steps {
		got, err := SnapshotStars(db, cid, s.stars, s.ts)
		if err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (s.want == nil) || (got != nil && *got != *s.want) {
			t.Errorf("%s: got %v want %v", s.ts, deref(got), deref(s.want))
		}
	}
	var stars int64
	var spd sql.NullFloat64
	db.QueryRow("SELECT stars, stars_per_day FROM scout_candidates WHERE id=?", cid).Scan(&stars, &spd)
	if stars != 111 || spd.Float64 != 11.0 {
		t.Errorf("stored %d %v", stars, spd)
	}
	if _, err := SnapshotStars(db, "C-9999", 1, ""); err == nil || err.Error() != "no candidate C-9999" {
		t.Errorf("err %v", err)
	}
	if _, err := SnapshotStars(db, cid, 1, "yesterday"); err == nil || !strings.Contains(err.Error(), "expected ISO 8601") {
		t.Errorf("err %v", err)
	}
}

func f(x float64) *float64 { return &x }
func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestCursors(t *testing.T) {
	db := newDB(t)
	if _, ok, err := GetCursor(db, "hn"); ok || err != nil {
		t.Fatal("cursor before set")
	}
	for _, v := range []string{"1", "2"} {
		if err := SetCursor(db, "hn", v, "2026-11-01T00:00:00+00:00"); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok, _ := GetCursor(db, "hn"); !ok || v != "2" {
		t.Errorf("cursor %q %v", v, ok)
	}
}

func TestParseRoles(t *testing.T) {
	valid := []string{"coder", "qa"}
	if got, err := ParseRoles(" qa, coder ,qa", valid); err != nil || strings.Join(got, ",") != "qa,coder" {
		t.Errorf("%v %v", got, err)
	}
	if got, err := ParseRoles("none", valid); err != nil || got[0] != "none" {
		t.Errorf("%v %v", got, err)
	}
	for in, msg := range map[string]string{
		"":          "--roles is required: a comma list of roles, or 'none'",
		"none,qa":   "--roles: 'none' cannot be combined with other roles",
		"qa,wizard": "--roles: unknown role(s) wizard; valid: coder, qa, none",
	} {
		if _, err := ParseRoles(in, valid); err == nil || err.Error() != msg {
			t.Errorf("ParseRoles(%q): %v, want %q", in, err, msg)
		}
	}
}

func TestAddTriageValidatesBeforeWriting(t *testing.T) {
	db := newDB(t)
	cid, _, _ := UpsertCandidate(db, "gh:a/b", "search", "2026-11-01T00:00:00+00:00", Facts{})
	valid := []string{"coder"}
	bad := Triage{CandidateID: cid, Purpose: "p", Roles: "coder", Overlap: "o", Rec: "adopt", Why: strings.Repeat("x", TextMax+1)}
	if _, err := AddTriage(db, bad, valid); err == nil || !strings.Contains(err.Error(), "--why is 601 chars") {
		t.Fatalf("err %v", err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM scout_triage").Scan(&n)
	if n != 0 {
		t.Fatal("a refused triage wrote a row")
	}
	ok := bad
	ok.Why, ok.SuggestEval, ok.TS = "fine", true, "2026-11-02T00:00:00+00:00"
	if _, err := AddTriage(db, ok, valid); err != nil {
		t.Fatal(err)
	}
	c, _, _ := GetCandidate(db, cid)
	if c.Status != "triaged" {
		t.Errorf("status %s", c.Status)
	}
	ok.CandidateID = "C-9999"
	if _, err := AddTriage(db, ok, valid); err == nil || err.Error() != "no candidate C-9999" {
		t.Errorf("err %v", err)
	}
	ok.CandidateID, ok.Rec = cid, "maybe"
	if _, err := AddTriage(db, ok, valid); err == nil || err.Error() != "--rec must be one of adopt, watch, skip" {
		t.Errorf("err %v", err)
	}
}

// The order is scout/db.py PENDING_ORDER; the expected ids are from the Python pending() on the same rows.
func TestPendingOrderAndList(t *testing.T) {
	db := newDB(t)
	ins := func(key, kind, risk, srcs, first string, spd any) {
		mustExec(t, db, "INSERT INTO scout_candidates(id,key,kind,install_risk,sources,first_seen,last_seen,stars_per_day) VALUES(?,?,?,?,?,?,?,?)",
			"C-"+strings.Repeat("0", 3)+string(rune('1'+countRows(t, db))), key, kind, risk, srcs, first, first, spd)
	}
	ins("gh:a/plugin-flagged", "plugin", "scripts", `["search"]`, "2026-11-01T00:00:00+00:00", nil)      // 1
	ins("gh:a/skill-clean", "skill", "prompt-only", `["marketplace"]`, "2026-11-01T00:00:00+00:00", 1.0) // 2
	ins("gh:a/skill-search", "skill", "prompt-only", `["search"]`, "2026-11-02T00:00:00+00:00", 1.0)     // 3
	ins("gh:a/skill-trend", "skill", "prompt-only", `["search"]`, "2026-11-03T00:00:00+00:00", 9.0)      // 4
	ins("gh:a/agent-flagged", "agent", "mcp", `["hn"]`, "2026-11-01T00:00:00+00:00", 5.0)                // 5
	ins(ControlKey, "skill", "prompt-only", `["x"]`, "2026-11-01T00:00:00+00:00", 99.0)                  // 6 (never pending)
	got, err := Pending(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, " ") != "C-0004 C-0003 C-0002 C-0005 C-0001" {
		t.Errorf("order %v", ids)
	}
	if lim, _ := Pending(db, 2); len(lim) != 2 {
		t.Errorf("limit gave %d", len(lim))
	}
	if _, err := Pending(db, 0); err == nil || err.Error() != "--limit must be at least 1, got 0" {
		t.Errorf("err %v", err)
	}
	if l, _ := ListCandidates(db, true, "", "scripts", ""); len(l) != 1 || l[0].ID != "C-0001" {
		t.Errorf("risk filter: %v", l)
	}
	if l, _ := ListCandidates(db, false, "", "", "2026-11-02"); len(l) != 2 {
		t.Errorf("since filter: %d rows", len(l))
	}
	if _, err := ListCandidates(db, false, "", "", "soon"); err == nil || !strings.Contains(err.Error(), "bad --since 'soon'") {
		t.Errorf("err %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM scout_candidates").Scan(&n)
	return n
}

func TestSetRiskAndNeedsOwner(t *testing.T) {
	db := newDB(t)
	cid, _, _ := UpsertCandidate(db, "gh:a/b", "search", "2026-11-01T00:00:00+00:00", Facts{})
	var detail []string
	for i := 0; i < 25; i++ {
		detail = append(detail, "d")
	}
	if err := SetRisk(db, cid, "scripts", detail); err != nil {
		t.Fatal(err)
	}
	c, _, _ := GetCandidate(db, cid)
	if c.InstallRisk.String != "scripts" || strings.Count(c.RiskDetail.String, `"d"`) != 20 {
		t.Errorf("%+v", c)
	}
	if err := SetNeedsOwner(db, cid, detail); err != nil {
		t.Fatal(err)
	}
	c, _, _ = GetCandidate(db, cid)
	if c.Status != "needs-owner" || !strings.HasSuffix(c.RiskDetail.String, `"... and 5 more"]`) {
		t.Errorf("%+v", c)
	}
}
