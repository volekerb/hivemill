package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Shared scoreboard helpers of bin/factory_cli.py: scorecard_rows, lead_state, lead_counts, day_hours, director_row,
// director_line, cap_breaches, digest_clock and _ts. `scorecard` uses them now; `digest` reuses them (G-041 S2r owns
// this file; later tasks call it and never edit it). Every function that cannot reproduce Python byte for byte returns
// errDelegate so the command runs in Python.

// sbOwnerCapKeys is OWNER_CAP_KEYS (Python line 132).
var sbOwnerCapKeys = []string{"max_live_agents", "max_total_claude", "min_mem_available_mb", "max_five_hour_pct",
	"max_seven_day_pct", "gate_max_load_ratio", "tmp_max_pct", "max_live_leads", "reserved_worker_slots", "lead_parking",
	"max_live_judges"}

// sbWorkerRoles is WORKER_ROLES (Python line 729).
var sbWorkerRoles = []string{"cleaner", "coder", "evaluator", "judge", "pm", "qa", "researcher", "retro", "reviewer",
	"scout", "staff-engineer"}

const sbCap = 5 * time.Minute

// sbIsoRe is the shape of a timestamp datetime.fromisoformat accepts that this port parses; sbTsRe captures its parts.
var sbTsRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2})(?::(\d{2})(?::(\d{2})(?:[.,](\d+))?)?)?(Z|[+-]\d{2}:\d{2}(?::\d{2})?)?)?$`)

// sbParseTS is Python's _ts: an ISO timestamp as an instant (naive = local time loc); ok=false is "ignore this value".
// A value that starts like a date but has a shape this parser does not know returns errDelegate.
func sbParseTS(s string, loc *time.Location) (t time.Time, ok bool, err error) {
	m := sbTsRe.FindStringSubmatch(s)
	if m == nil {
		if len(s) > 0 && (s[0] >= '0' && s[0] <= '9' || s[0] > 0x7f) {
			return t, false, errDelegate
		}
		return t, false, nil
	}
	atoi := func(x string) int { n, _ := strconv.Atoi(x); return n }
	frac := 0
	if m[7] != "" {
		f := (m[7] + "000000")[:6]
		frac = atoi(f)
	}
	hh, mm, ss := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if hh > 23 || mm > 59 || ss > 59 {
		return t, false, nil
	}
	l := loc
	if z := m[8]; z != "" {
		off := 0
		if z != "Z" {
			sign := 1
			if z[0] == '-' {
				sign = -1
			}
			oh, om := atoi(z[1:3]), atoi(z[4:6])
			os := 0
			if len(z) > 6 {
				os = atoi(z[7:9])
			}
			if oh > 23 || om > 59 || os > 59 {
				return t, false, nil
			}
			off = sign * (oh*3600 + om*60 + os)
		}
		l = time.FixedZone("", off)
	}
	if atoi(m[1]) == 0 {
		return t, false, nil // Python: year 0 is out of range
	}
	t = time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), hh, mm, ss, frac*1000, l)
	if t.Year() != atoi(m[1]) || int(t.Month()) != atoi(m[2]) || t.Day() != atoi(m[3]) {
		return t, false, nil // an impossible date such as 2026-02-30
	}
	return t, true, nil
}

// sbLocation is the zone Python's astimezone() uses: the TZ of the command's environment, else the process zone.
func sbLocation(getenv func(string) string) (*time.Location, error) {
	tz := strings.TrimPrefix(getenv("TZ"), ":")
	if tz == "" {
		return time.Now().Location(), nil // the process zone
	}
	l, err := time.LoadLocation(tz)
	if err != nil {
		return nil, errDelegate
	}
	return l, nil
}

// sbFixed is t in the fixed offset its zone has at t, like Python's aware datetime after astimezone().
func sbFixed(t time.Time, loc *time.Location) time.Time {
	_, off := t.In(loc).Zone()
	return t.In(time.FixedZone("", off))
}

// sbClock is digest_clock: $FACTORY_NOW (naive = local) else now. An invalid $FACTORY_NOW delegates (Python dies with a
// traceback after the table was printed, which Go does not reproduce).
func sbClock(getenv func(string) string, now func() time.Time, loc *time.Location) (time.Time, error) {
	fixed := getenv("FACTORY_NOW")
	if fixed == "" {
		return sbFixed(now(), loc), nil
	}
	t, ok, err := sbParseTS(fixed, loc)
	if err != nil || !ok {
		return t, errDelegate
	}
	return sbFixed(t, loc), nil
}

func sbISO(t time.Time) string { return t.Format("2006-01-02T15:04:05-07:00") }

// pyIntStr is int(s) for a string: optional whitespace and sign, ASCII digits with single underscores between them.
// ok=false is a ValueError; non-ASCII input returns errDelegate (Python accepts other decimal digits).
func pyIntStr(s string) (n int64, ok bool, err error) {
	for _, r := range s {
		if r > 0x7f && !pyIsSpace(r) {
			return 0, false, errDelegate
		}
	}
	s = pyStrip(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return 0, false, nil
	}
	s = strings.ReplaceAll(s, "_", "")
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false, nil
		}
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil {
		return 0, false, errDelegate
	}
	if neg {
		v = -v
	}
	return v, true, nil
}

// sbCfg is cfg(con, key): the config row, else the built-in default.
func sbCfg(db *sql.DB, key string) (any, error) {
	var v any
	err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	if err == sql.ErrNoRows {
		d, ok := defaultMap()[key]
		if !ok {
			return nil, errDelegate // Python raises KeyError
		}
		return d, nil
	}
	if err != nil {
		return nil, errDelegate
	}
	return v, nil
}

func sbCount(db *sql.DB, q string, args ...any) (int, error) {
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, errDelegate
	}
	return n, nil
}

// sbLeadState is lead_state for a teams row: live | parked | queued | -.
func sbLeadState(db *sql.DB, leadAgent, parkedAt sql.NullString, status string) (string, error) {
	if leadAgent.Valid && leadAgent.String != "" {
		var st string
		err := db.QueryRow("SELECT status FROM agents WHERE name=?", leadAgent.String).Scan(&st)
		if err != nil && err != sql.ErrNoRows {
			return "", errDelegate
		}
		if err == nil && (st == "live" || st == "starting") {
			return "live", nil
		}
		if err == nil && st == "queued" {
			return "queued", nil
		}
	}
	if parkedAt.Valid && parkedAt.String != "" && status == "active" {
		return "parked", nil
	}
	return "-", nil
}

// LeadCounts is lead_counts: workers live, leads live, parked teams, wake-queued leads, HQ live.
type LeadCounts struct{ Workers, Leads, Parked, Queued, HQ int }

func sbLeadCounts(db *sql.DB) (LeadCounts, error) {
	var lc LeadCounts
	marks := strings.TrimSuffix(strings.Repeat("?,", len(sbWorkerRoles)), ",")
	args := make([]any, len(sbWorkerRoles))
	for i, r := range sbWorkerRoles {
		args[i] = r
	}
	var err error
	if lc.Workers, err = sbCount(db, "SELECT count(*) c FROM agents WHERE status='live' AND role IN ("+marks+")", args...); err != nil {
		return lc, err
	}
	if lc.Leads, err = sbCount(db, "SELECT count(*) c FROM agents WHERE role='lead' AND status='live'"); err != nil {
		return lc, err
	}
	if lc.Parked, err = sbCount(db, "SELECT count(*) c FROM teams WHERE status='active' AND parked_at IS NOT NULL"); err != nil {
		return lc, err
	}
	if lc.Queued, err = sbCount(db, "SELECT count(*) c FROM agents WHERE role='lead' AND status='queued'"); err != nil {
		return lc, err
	}
	lc.HQ, err = sbCount(db, "SELECT count(*) c FROM agents WHERE status='live' AND role IN ('boss','director','lead')")
	return lc, err
}

// ScoreRow is one scorecard_rows entry, already stringified for the table.
type ScoreRow struct{ Team, Goal, Status, Tasks, Done, Merged, Rejections, Retries, Score, Lead string }

func sbScoreRows(db *sql.DB) ([]ScoreRow, error) {
	type team struct {
		id, goal, status string
		lead, parked     sql.NullString
	}
	rs, err := db.Query("SELECT id, goal_id, status, lead_agent, parked_at FROM teams ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	var teams []team
	for rs.Next() {
		var t team
		if err := rs.Scan(&t.id, &t.goal, &t.status, &t.lead, &t.parked); err != nil {
			rs.Close()
			return nil, errDelegate
		}
		teams = append(teams, t)
	}
	rs.Close()
	if rs.Err() != nil {
		return nil, errDelegate
	}
	var rows []ScoreRow
	for _, t := range teams {
		tr, err := db.Query("SELECT id,status,attempts FROM tasks WHERE team_id=? OR (team_id IS NULL AND goal_id=?)", t.id, t.goal)
		if err != nil {
			return nil, errDelegate
		}
		var ids []any
		done, retries := 0, 0
		for tr.Next() {
			var id, st string
			var att int
			if err := tr.Scan(&id, &st, &att); err != nil {
				tr.Close()
				return nil, errDelegate
			}
			ids = append(ids, id)
			if st == "done" {
				done++
			}
			if att > 1 {
				retries += att - 1
			}
		}
		tr.Close()
		if tr.Err() != nil {
			return nil, errDelegate
		}
		n := len(ids)
		if n == 0 {
			ids = []any{""}
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		fails, err := sbCount(db, "SELECT count(*) c FROM verdicts WHERE task_id IN ("+ph+") AND verdict IN ('fail','reject')", ids...)
		if err != nil {
			return nil, err
		}
		merged, err := sbCount(db, "SELECT count(*) c FROM verdicts WHERE task_id IN ("+ph+") AND verdict='merge'", ids...)
		if err != nil {
			return nil, err
		}
		var score sql.NullFloat64
		if err := db.QueryRow("SELECT round(avg(score),1) s FROM evaluations WHERE team_id=?", t.id).Scan(&score); err != nil {
			return nil, errDelegate
		}
		sc := ""
		if score.Valid {
			sc = pyFloatRepr(score.Float64)
		}
		lead, err := sbLeadState(db, t.lead, t.parked, t.status)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ScoreRow{t.id, t.goal, t.status, strconv.Itoa(n), strconv.Itoa(done), strconv.Itoa(merged),
			strconv.Itoa(fails), strconv.Itoa(retries), sc, lead})
	}
	return rows, nil
}

// sbDayHours is day_hours: the local daytime window, (8, 20) when the config is unusable.
func sbDayHours(db *sql.DB) (int, int, error) {
	var v [2]int64
	for i, k := range []string{"day_start_hour", "day_end_hour"} {
		raw, err := sbCfg(db, k)
		if err != nil {
			return 0, 0, err
		}
		var s string
		switch x := raw.(type) {
		case string:
			s = x
		case int64:
			s = strconv.FormatInt(x, 10)
		case nil:
			return 8, 20, nil // int(None) is a TypeError, which day_hours catches
		default:
			return 0, 0, errDelegate
		}
		n, ok, err := pyIntStr(s)
		if err != nil {
			return 0, 0, err
		}
		if !ok {
			return 8, 20, nil
		}
		v[i] = n
	}
	if 0 <= v[0] && v[0] < v[1] && v[1] <= 24 {
		return int(v[0]), int(v[1]), nil
	}
	return 8, 20, nil
}

// DirectorRow is the director_row dict. HasSamples is Python's truthiness of row["samples"].
type DirectorRow struct {
	Hours, DayStart, DayEnd, Samples, Breaches int
	AvgWorkers                                 float64
	Share, Target                              int
	MedianQ                                    string
	StarvedMin, Longest                        int
	LongestGoal                                string
	Scores                                     []int
}

type sbSample struct {
	t                     time.Time
	live, workers, queued int64
	target                int64
	p1                    sql.NullString
}

var sbScoreRe = regexp.MustCompile(`^director score (\d{4}-\d{2}-\d{2}): ([1-5])/5$`)

// sbDirectorRow is director_row(con, start, end, hours): start and end share one fixed offset (sbClock); loc is the
// zone astimezone() maps to for the daytime-hour filter and the score date.
func sbDirectorRow(db *sql.DB, start, end time.Time, hours int, loc *time.Location) (DirectorRow, error) {
	var row DirectorRow
	ds, de, err := sbDayHours(db)
	if err != nil {
		return row, err
	}
	slack := sbISO(start.Add(-24 * time.Hour))
	rs, err := db.Query("SELECT ts, live, workers, queued, target, p1_starved FROM util_samples WHERE ts>=? ORDER BY ts, id", slack)
	if err != nil {
		return row, errDelegate
	}
	var samples []sbSample
	for rs.Next() {
		var s sbSample
		var ts string
		if err := rs.Scan(&ts, &s.live, &s.workers, &s.queued, &s.target, &s.p1); err != nil {
			rs.Close()
			return row, errDelegate
		}
		t, ok, perr := sbParseTS(ts, loc)
		if perr != nil {
			rs.Close()
			return row, perr
		}
		if !ok {
			continue
		}
		h := t.In(loc).Hour()
		if !t.Before(start) && t.Before(end) && ds <= h && h < de {
			s.t = t
			samples = append(samples, s)
		}
	}
	rs.Close()
	if rs.Err() != nil {
		return row, errDelegate
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].t.Before(samples[j].t) })
	us := func(d time.Duration) int64 { return int64(d / time.Microsecond) }
	weights := make([]float64, len(samples))
	for i := range samples {
		if i+1 < len(samples) {
			d := samples[i+1].t.Sub(samples[i].t)
			if d > sbCap {
				d = sbCap
			}
			weights[i] = float64(us(d)) / 1e6 / 60
		} else {
			weights[i] = 1.0
		}
	}
	nb, err := sbBreaches(db, sbISO(start), sbISO(end))
	if err != nil {
		return row, err
	}
	row.Hours, row.DayStart, row.DayEnd, row.Samples, row.Breaches = hours, ds, de, len(samples), nb
	if len(samples) > 0 {
		var tot, wsum, ssum float64
		for i, s := range samples {
			tot += weights[i]
			wsum += weights[i] * float64(s.workers)
			share := 0.0
			if s.live != 0 {
				share = float64(s.workers) / float64(s.live)
			}
			ssum += weights[i] * share
		}
		row.AvgWorkers = wsum / tot
		row.Share = int(math.RoundToEven(100 * ssum / tot))
		row.Target = int(samples[len(samples)-1].target)
		var qw []int
		for _, s := range samples {
			if s.queued > 0 {
				qw = append(qw, int(s.workers))
			}
		}
		sort.Ints(qw)
		mid := len(qw) / 2
		switch {
		case len(qw) == 0:
			row.MedianQ = "-"
		case len(qw)%2 == 1:
			row.MedianQ = strconv.Itoa(qw[mid])
		default:
			v := float64(qw[mid-1]+qw[mid]) / 2
			if math.Abs(v) >= 1e6 {
				return row, errDelegate // :g would switch to exponent form
			}
			row.MedianQ = strconv.FormatFloat(v, 'f', -1, 64)
		}
		type ep struct {
			g string
			v float64
		}
		starved, longest := 0.0, 0.0
		longestG := "-"
		var run []ep
		for i, s := range samples {
			ids, err := sbStarvedIDs(s.p1)
			if err != nil {
				return row, err
			}
			starved += weights[i] * float64(len(ids))
			if i > 0 && s.t.Sub(samples[i-1].t) > sbCap {
				run = nil
			}
			prev := map[string]float64{}
			for _, e := range run {
				prev[e.g] = e.v
			}
			run = run[:0:0]
			seen := map[string]bool{}
			for _, g := range ids {
				if seen[g] {
					continue
				}
				seen[g] = true
				run = append(run, ep{g, prev[g] + weights[i]})
			}
			for _, e := range run {
				if e.v > longest {
					longest, longestG = e.v, e.g
				}
			}
		}
		row.StarvedMin, row.Longest, row.LongestGoal = int(math.RoundToEven(starved)), int(math.RoundToEven(longest)), longestG
	}
	endDate := end.In(loc).Format("2006-01-02")
	ms, err := db.Query("SELECT subject FROM messages WHERE recipient='director' ORDER BY id")
	if err != nil {
		return row, errDelegate
	}
	defer ms.Close()
	scores := map[string]int{}
	for ms.Next() {
		var subj sql.NullString
		if err := ms.Scan(&subj); err != nil {
			return row, errDelegate
		}
		m := sbScoreRe.FindStringSubmatch(strings.TrimSuffix(subj.String, "\n"))
		if m == nil {
			if strings.HasPrefix(subj.String, "director score ") && sbHasNonASCII(subj.String) {
				return row, errDelegate // \d also matches other Unicode digits in Python
			}
			continue
		}
		if m[1] <= endDate {
			scores[m[1]], _ = strconv.Atoi(m[2])
		}
	}
	if ms.Err() != nil {
		return row, errDelegate
	}
	days := make([]string, 0, len(scores))
	for d := range scores {
		days = append(days, d)
	}
	sort.Strings(days)
	if len(days) > 2 {
		days = days[len(days)-2:]
	}
	for _, d := range days {
		row.Scores = append(row.Scores, scores[d])
	}
	return row, nil
}

func sbHasNonASCII(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return true
		}
	}
	return false
}

// sbStarvedIDs is json.loads(p1_starved or "[]") as a list of goal ids; invalid JSON is []. Anything else Python would
// iterate in a way this port does not reproduce (a non-list, non-string members, NaN) delegates.
func sbStarvedIDs(raw sql.NullString) ([]string, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw.String), &v); err != nil {
		if strings.Contains(raw.String, "NaN") || strings.Contains(raw.String, "Infinity") {
			return nil, errDelegate
		}
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errDelegate
	}
	ids := make([]string, len(list))
	for i, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, errDelegate
		}
		ids[i] = s
	}
	return ids, nil
}

// sbBreaches is len(cap_breaches(con, start_iso, end_iso)): owner-cap config sets made by a factory agent.
func sbBreaches(db *sql.DB, startISO, endISO string) (int, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(sbOwnerCapKeys)), ",")
	args := make([]any, 0, len(sbOwnerCapKeys)+2)
	for _, k := range sbOwnerCapKeys {
		args = append(args, k)
	}
	args = append(args, startISO, endISO)
	return sbCount(db, "SELECT count(*) FROM events WHERE entity='config' AND action='set' AND entity_id IN ("+marks+
		") AND ts>=? AND ts<? AND actor IN (SELECT name FROM agents)", args...)
}

// sbDirectorLine is director_line(row).
func sbDirectorLine(r DirectorRow) string {
	head := fmt.Sprintf("director (%dh, daytime %02d-%02d): ", r.Hours, r.DayStart, r.DayEnd)
	sc := "-"
	if len(r.Scores) > 0 {
		parts := make([]string, len(r.Scores))
		for i, s := range r.Scores {
			parts[i] = strconv.Itoa(s)
		}
		sc = strings.Join(parts, ", ")
	}
	tail := fmt.Sprintf("cap breaches %d · boss scores %s", r.Breaches, sc)
	if len(r.Scores) == 2 && max(r.Scores[0], r.Scores[1]) <= 2 {
		tail += " · TWO LOW SCORES"
	}
	if r.Samples == 0 {
		return head + "no utilization samples · " + tail
	}
	return head + fmt.Sprintf("avg workers %.1f · share %d%% · target %d · median workers with queue %s · P1 starved %d min "+
		"(longest %d min %s) · ", r.AvgWorkers, r.Share, r.Target, r.MedianQ, r.StarvedMin, r.Longest, r.LongestGoal) + tail
}
