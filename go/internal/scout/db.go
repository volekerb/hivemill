package scout

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Port of scout/db.py: candidate upsert/dedupe, star trend, cursors, triage. Every function takes an open
// *sql.DB and binds every value: candidate text (READMEs, descriptions) is untrusted input. The scout tables
// are created by Python (SCOUT_DDL stays there until P1q/P1-L2): Go never runs DDL here. The functions that
// open a transaction (BEGIN IMMEDIATE, in schedule.go and report.go) need a single-connection pool, as
// factorydb.Open gives.

const (
	ControlKey = "local:skilleval-control" // the skilleval positive control: a reserved row, never a find
	TextMax    = 600
	TrendDays  = 14
)

var Recs = []string{"adopt", "watch", "skip"}

// ScoutError is invalid input; the CLI maps it to exit 2.
type ScoutError struct{ Msg string }

func (e *ScoutError) Error() string { return e.Msg }

func scoutErr(format string, a ...any) error { return &ScoutError{fmt.Sprintf(format, a...)} }

// localLoc is the zone of naive stamps (local time); tests replace it.
var localLoc = time.Now().Location()

// Now is the clock for stamps; tests replace it.
var Now = time.Now

// Stamp is isoformat(timespec="seconds"): 2026-11-14T22:13:26+01:00 (Python prints +00:00 for UTC, not Z).
func Stamp(t time.Time) string { return t.Format("2006-01-02T15:04:05-07:00") }

func nowStamp() string { return Stamp(Now()) }

// ParseTS is _parse_ts: ISO text to an aware time. A naive stamp is local time, so aware and naive rows compare.
func ParseTS(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "Z") || strings.HasSuffix(s, "z") {
		s = s[:len(s)-1] + "+00:00"
	}
	s = strings.Replace(s, " ", "T", 1)
	for _, l := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04:05-07:00", "2006-01-02T15:04-07:00", "2006-01-02-07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02T15", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, s, localLoc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad ISO timestamp %q", s)
}

// NormalizeKey lowercases owner/repo, strips a trailing ".git" and "/". A path after owner/repo keeps its case.
func NormalizeKey(key string) (string, error) {
	k := pyTrim(key)
	if k == "" || !strings.Contains(k, ":") {
		return "", scoutErr("bad candidate key %s: expected 'gh:<owner>/<repo>' or 'mkt:<marketplace>/<plugin>'", pyRepr(key))
	}
	scheme, rest, _ := strings.Cut(k, ":")
	scheme = strings.ToLower(scheme)
	rest = strings.TrimRight(pyTrim(rest), "/")
	if rest == "" {
		return "", scoutErr("bad candidate key %s", pyRepr(key))
	}
	if scheme == "gh" {
		parts := strings.Split(rest, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", scoutErr("bad candidate key %s: expected gh:<owner>/<repo>[/<path>]", pyRepr(key))
		}
		parts[1] = strings.TrimSuffix(parts[1], ".git")
		parts[0], parts[1] = strings.ToLower(parts[0]), strings.ToLower(parts[1])
		rest = strings.Join(parts, "/")
	} else {
		rest = strings.ToLower(rest)
	}
	return scheme + ":" + rest, nil
}

// pyRepr is repr() of a str: single quotes unless the text holds a single and no double quote; backslash, quote,
// \n \r \t and non-printable characters escaped.
func pyRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var sb strings.Builder
	sb.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, r)
		case !unicode.IsPrint(r) && r != ' ':
			switch {
			case r < 0x100:
				fmt.Fprintf(&sb, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&sb, `\u%04x`, r)
			default:
				fmt.Fprintf(&sb, `\U%08x`, r)
			}
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte(q)
	return sb.String()
}

// Facts are the optional candidate columns (FACTS); a nil field keeps the old value.
type Facts struct {
	Kind, Repo, Path, URL, Licence, LastCommit, DefaultBranch, HeadSha, InstallRisk, RiskDetail *string
	Stars                                                                                       *int64
}

// S returns a pointer to s, for Facts.
func S(s string) *string { return &s }

// I returns a pointer to n, for Facts.
func I(n int64) *int64 { return &n }

func (f Facts) columns() (cols []string, vals []any) {
	add := func(c string, v any, ok bool) {
		if ok {
			cols, vals = append(cols, c), append(vals, v)
		}
	}
	sp := func(c string, p *string) {
		if p != nil {
			add(c, *p, true)
		}
	}
	sp("kind", f.Kind)
	sp("repo", f.Repo)
	sp("path", f.Path)
	sp("url", f.URL)
	sp("licence", f.Licence)
	sp("last_commit", f.LastCommit)
	sp("default_branch", f.DefaultBranch)
	sp("head_sha", f.HeadSha)
	if f.Stars != nil {
		add("stars", *f.Stars, true)
	}
	sp("install_risk", f.InstallRisk)
	sp("risk_detail", f.RiskDetail)
	return
}

// JSONList encodes a list the way json.dumps does for a risk_detail value.
func JSONList(items []string) string {
	if items == nil {
		items = []string{}
	}
	return pyDumps(items)
}

func nextCandidateID(db *sql.DB) (string, error) {
	rows, err := db.Query("SELECT id FROM scout_candidates")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if _, num, ok := strings.Cut(id, "-"); ok {
			if v, err := strconv.Atoi(num); err == nil && v > n {
				n = v
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("C-%04d", n+1), nil
}

// UpsertCandidate: a new key gets a C-nnnn row, status new. A known key gets its facts updated (nil keeps the old
// value), last_seen set, seen_count+1 and the source merged into the sources JSON. ts "" means now.
func UpsertCandidate(db *sql.DB, key, source, ts string, f Facts) (id string, created bool, err error) {
	if strings.TrimSpace(source) == "" {
		return "", false, scoutErr("source is required")
	}
	if key, err = NormalizeKey(key); err != nil {
		return "", false, err
	}
	if ts == "" {
		ts = nowStamp()
	}
	cols, vals := f.columns()
	var rowID, sourcesJSON string
	err = db.QueryRow("SELECT id, sources FROM scout_candidates WHERE key=?", key).Scan(&rowID, &sourcesJSON)
	switch {
	case err == nil:
		var srcs []string
		if sourcesJSON != "" {
			if jerr := json.Unmarshal([]byte(sourcesJSON), &srcs); jerr != nil {
				return "", false, fmt.Errorf("candidate %s has unreadable sources JSON: %w", rowID, jerr)
			}
		}
		found := false
		for _, s := range srcs {
			found = found || s == source
		}
		if !found {
			srcs = append(srcs, source)
		}
		sets := []string{"sources=?", "last_seen=?", "seen_count=seen_count+1"}
		args := []any{pyDumps(srcs), ts}
		for _, c := range cols {
			sets = append(sets, c+"=?")
		}
		args = append(append(args, vals...), rowID)
		if _, err := db.Exec("UPDATE scout_candidates SET "+strings.Join(sets, ", ")+" WHERE id=?", args...); err != nil {
			return "", false, err
		}
		return rowID, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", false, err
	}
	cid, err := nextCandidateID(db)
	if err != nil {
		return "", false, err
	}
	all := append([]string{"id", "key", "sources", "first_seen", "last_seen"}, cols...)
	args := append([]any{cid, key, pyDumps([]string{source}), ts, ts}, vals...)
	q := "INSERT INTO scout_candidates(" + strings.Join(all, ",") + ") VALUES(" + strings.TrimSuffix(strings.Repeat("?,", len(all)), ",") + ")"
	if _, err := db.Exec(q, args...); err != nil {
		return "", false, err
	}
	return cid, true, nil
}

func round4(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 4, 64), 64)
	return v
}

// SnapshotStars records a star count and recomputes stars_per_day from the first and latest snapshot within
// TrendDays of the latest one (nil while there is only one snapshot or no time between them).
func SnapshotStars(db *sql.DB, cid string, stars int64, ts string) (*float64, error) {
	var stamp string
	if ts == "" {
		stamp = nowStamp()
	} else {
		t, err := ParseTS(ts)
		if err != nil {
			return nil, scoutErr("bad star snapshot timestamp %s: expected ISO 8601", pyRepr(ts))
		}
		stamp = Stamp(t)
	}
	var one int
	if err := db.QueryRow("SELECT 1 FROM scout_candidates WHERE id=?", cid).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, scoutErr("no candidate %s", cid)
		}
		return nil, err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO scout_stars(candidate_id, ts, stars) VALUES(?,?,?)", cid, stamp, stars); err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT ts, stars FROM scout_stars WHERE candidate_id=? ORDER BY julianday(ts), ts", cid)
	if err != nil {
		return nil, err
	}
	type snap struct {
		t time.Time
		s int64
	}
	var snaps []snap
	for rows.Next() {
		var rts string
		var rs int64
		if err := rows.Scan(&rts, &rs); err != nil {
			rows.Close()
			return nil, err
		}
		t, perr := ParseTS(rts)
		if perr != nil {
			rows.Close()
			return nil, scoutErr("scout_stars of %s holds an unreadable timestamp %s", cid, pyRepr(rts))
		}
		snaps = append(snaps, snap{t, rs})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	last := snaps[len(snaps)-1]
	first := last
	for _, s := range snaps {
		if last.t.Sub(s.t) <= TrendDays*24*time.Hour {
			first = s
			break
		}
	}
	days := last.t.Sub(first.t).Seconds() / 86400
	var rate *float64
	var arg any
	if days > 0 {
		r := round4(float64(last.s-first.s) / days)
		rate, arg = &r, r
	}
	if _, err := db.Exec("UPDATE scout_candidates SET stars=?, stars_per_day=? WHERE id=?", last.s, arg, cid); err != nil {
		return nil, err
	}
	return rate, nil
}

// GetCursor returns the cursor of a source and whether one is stored (the column may hold NULL: ok, "").
func GetCursor(db *sql.DB, source string) (string, bool, error) {
	var c sql.NullString
	err := db.QueryRow("SELECT cursor FROM scout_cursors WHERE source=?", source).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return c.String, err == nil, err
}

// SetCursor stores the cursor of a source; ts "" means now.
func SetCursor(db *sql.DB, source, cursor, ts string) error {
	if ts == "" {
		ts = nowStamp()
	}
	_, err := db.Exec("INSERT INTO scout_cursors(source, cursor, updated) VALUES(?,?,?) "+
		"ON CONFLICT(source) DO UPDATE SET cursor=excluded.cursor, updated=excluded.updated", source, cursor, ts)
	return err
}

func triageText(name, val string) error {
	if strings.TrimSpace(val) == "" {
		return scoutErr("--%s is required and may not be empty", name)
	}
	if n := runeLen(val); n > TextMax {
		return scoutErr("--%s is %d chars, the limit is %d", name, n, TextMax)
	}
	return nil
}

// ParseRoles validates a comma list of roles, or "none".
func ParseRoles(val string, valid []string) ([]string, error) {
	var parts []string
	for _, p := range strings.Split(val, ",") {
		if p = pyTrim(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return nil, scoutErr("--roles is required: a comma list of roles, or 'none'")
	}
	for _, p := range parts {
		if p == "none" {
			if len(parts) != 1 {
				return nil, scoutErr("--roles: 'none' cannot be combined with other roles")
			}
			return []string{"none"}, nil
		}
	}
	var bad []string
	for _, p := range parts {
		if !contains(valid, p) {
			bad = append(bad, p)
		}
	}
	if len(bad) > 0 {
		return nil, scoutErr("--roles: unknown role(s) %s; valid: %s, none", strings.Join(bad, ", "), strings.Join(valid, ", "))
	}
	var out []string
	for _, p := range parts {
		if !contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Triage is the input of AddTriage.
type Triage struct {
	CandidateID, Purpose, Roles, Overlap, Rec, Why string
	SuggestEval                                    bool
	RunID                                          *int64
	Author, TS                                     string
}

// AddTriage validates everything first and writes nothing on error. It sets a "new" candidate to "triaged".
func AddTriage(db *sql.DB, t Triage, validRoles []string) (int64, error) {
	if !contains(Recs, t.Rec) {
		return 0, scoutErr("--rec must be one of %s", strings.Join(Recs, ", "))
	}
	roles, err := ParseRoles(t.Roles, validRoles)
	if err != nil {
		return 0, err
	}
	for _, f := range [][2]string{{"purpose", t.Purpose}, {"overlap", t.Overlap}, {"why", t.Why}} {
		if err := triageText(f[0], f[1]); err != nil {
			return 0, err
		}
	}
	var one int
	if err := db.QueryRow("SELECT 1 FROM scout_candidates WHERE id=?", t.CandidateID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, scoutErr("no candidate %s", t.CandidateID)
		}
		return 0, err
	}
	ts := t.TS
	if ts == "" {
		ts = nowStamp()
	}
	ev := 0
	if t.SuggestEval {
		ev = 1
	}
	var runID, author any
	if t.RunID != nil {
		runID = *t.RunID
	}
	if t.Author != "" {
		author = t.Author
	}
	res, err := db.Exec("INSERT INTO scout_triage(candidate_id, purpose, target_roles, overlap, recommendation, rationale, "+
		"eval_suggested, run_id, author, created) VALUES(?,?,?,?,?,?,?,?,?,?)",
		t.CandidateID, t.Purpose, strings.Join(roles, ","), t.Overlap, t.Rec, t.Why, ev, runID, author, ts)
	if err != nil {
		return 0, err
	}
	if _, err := db.Exec("UPDATE scout_candidates SET status='triaged' WHERE id=? AND status='new'", t.CandidateID); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Candidate is a scout_candidates row.
type Candidate struct {
	ID, Key                                                                                     string
	Kind, Repo, Path, URL, Licence, LastCommit, DefaultBranch, HeadSha, InstallRisk, RiskDetail sql.NullString
	Stars                                                                                       sql.NullInt64
	StarsPerDay                                                                                 sql.NullFloat64
	Sources, FirstSeen, LastSeen, Status                                                        string
	SeenCount                                                                                   int64
	Rec                                                                                         sql.NullString // latest triage recommendation (ListCandidates)
}

const candCols = "id, key, kind, repo, path, url, licence, last_commit, default_branch, head_sha, stars, stars_per_day, " +
	"install_risk, risk_detail, sources, first_seen, last_seen, seen_count, status"

func (c *Candidate) dest() []any {
	return []any{&c.ID, &c.Key, &c.Kind, &c.Repo, &c.Path, &c.URL, &c.Licence, &c.LastCommit, &c.DefaultBranch, &c.HeadSha,
		&c.Stars, &c.StarsPerDay, &c.InstallRisk, &c.RiskDetail, &c.Sources, &c.FirstSeen, &c.LastSeen, &c.SeenCount, &c.Status}
}

func queryCandidates(db *sql.DB, withRec bool, q string, args ...any) ([]Candidate, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		d := c.dest()
		if withRec {
			d = append(d, &c.Rec)
		}
		if err := rows.Scan(d...); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCandidate returns the candidate with this id (ok false when absent).
func GetCandidate(db *sql.DB, id string) (Candidate, bool, error) {
	cs, err := queryCandidates(db, false, "SELECT "+candCols+" FROM scout_candidates WHERE id=?", id)
	if err != nil || len(cs) == 0 {
		return Candidate{}, false, err
	}
	return cs[0], true, nil
}

// PendingOrder is the triage order (docs/scout-pending.md): skills before vendor plugins, prompt-only before
// flagged, seen outside the marketplace source first, then trending, then oldest.
const PendingOrder = "CASE WHEN c.kind IN ('skill','collection','agent') THEN 0 ELSE 1 END, " +
	"CASE WHEN c.install_risk = 'prompt-only' THEN 0 ELSE 1 END, " +
	"CASE WHEN EXISTS (SELECT 1 FROM json_each(c.sources) j WHERE j.value <> 'marketplace') THEN 0 ELSE 1 END, " +
	"c.stars_per_day IS NULL, c.stars_per_day DESC, julianday(c.first_seen), c.id"

// Pending lists untriaged new candidates in PendingOrder, at most limit.
func Pending(db *sql.DB, limit int) ([]Candidate, error) {
	if limit < 1 {
		return nil, scoutErr("--limit must be at least 1, got %d", limit)
	}
	cols := "c." + strings.ReplaceAll(candCols, ", ", ", c.")
	return queryCandidates(db, false, "SELECT "+cols+" FROM scout_candidates c WHERE NOT EXISTS "+
		"(SELECT 1 FROM scout_triage t WHERE t.candidate_id=c.id) AND c.status='new' AND c.key<>? ORDER BY "+PendingOrder+" LIMIT ?",
		ControlKey, limit)
}

// InventoryItem is one skill or plugin under roles/plugins.
type InventoryItem struct{ Kind, Name, Dir string }

// ListCandidates is list_candidates: filters on new, latest recommendation, risk flag and first-seen instant.
func ListCandidates(db *sql.DB, onlyNew bool, rec, risk, since string) ([]Candidate, error) {
	cols := "c." + strings.ReplaceAll(candCols, ", ", ", c.")
	q := "SELECT " + cols + ", (SELECT recommendation FROM scout_triage t WHERE t.candidate_id=c.id ORDER BY t.id DESC LIMIT 1) AS rec " +
		"FROM scout_candidates c WHERE 1=1"
	var args []any
	if onlyNew {
		q += " AND c.status='new' AND c.key<>?"
		args = append(args, ControlKey)
	}
	if since != "" {
		t, err := ParseTS(since)
		if err != nil {
			return nil, scoutErr("bad --since %s: expected an ISO 8601 date or timestamp", pyRepr(since))
		}
		q += " AND julianday(c.first_seen)>=julianday(?)"
		args = append(args, Stamp(t))
	}
	rows, err := queryCandidates(db, true, q+" ORDER BY c.id", args...)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range rows {
		if rec != "" && r.Rec.String != rec {
			continue
		}
		if risk != "" && !contains(strings.Split(r.InstallRisk.String, ","), risk) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// SetRisk stores a fresh install-risk classification (detail: at most 20 strings).
func SetRisk(db *sql.DB, cid, risk string, detail []string) error {
	if len(detail) > 20 {
		detail = detail[:20]
	}
	_, err := db.Exec("UPDATE scout_candidates SET install_risk=?, risk_detail=? WHERE id=?", risk, JSONList(detail), cid)
	return err
}

// SetNeedsOwner holds the candidate for the owner and records the pre-screen hits (at most 20) in risk_detail.
func SetNeedsOwner(db *sql.DB, cid string, hitLines []string) error {
	shown := hitLines
	if len(shown) > 20 {
		shown = shown[:20]
	}
	shown = append([]string{}, shown...)
	if len(hitLines) > 20 {
		shown = append(shown, fmt.Sprintf("... and %d more", len(hitLines)-20))
	}
	_, err := db.Exec("UPDATE scout_candidates SET status='needs-owner', risk_detail=? WHERE id=?", JSONList(shown), cid)
	return err
}

// Inventory is inventory(): skill and plugin names under roles/plugins/**, as [(kind, name, relative dir)].
func Inventory(pluginsDir string) []InventoryItem {
	var skills, plugins []InventoryItem
	st, err := os.Stat(pluginsDir)
	if err != nil || !st.IsDir() {
		return nil
	}
	_ = filepath.WalkDir(pluginsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // unreadable subtrees are skipped, like Path.rglob
		}
		dir := filepath.Dir(p)
		rel, rerr := filepath.Rel(pluginsDir, dir)
		if rerr != nil {
			return nil
		}
		switch d.Name() {
		case "SKILL.md":
			skills = append(skills, InventoryItem{"skill", filepath.Base(dir), rel})
		case "plugin.json":
			if filepath.Base(dir) == ".claude-plugin" {
				parent := filepath.Dir(dir)
				prel, _ := filepath.Rel(pluginsDir, parent)
				plugins = append(plugins, InventoryItem{"plugin", filepath.Base(parent), prel})
			}
		}
		return nil
	})
	return append(skills, plugins...)
}
