package cli

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/identity"
)

// digest [--date D] [--hours N] and digest --send [--goal G] [--date D] [--hours N] (Python build_digest without a goal,
// cmd_digest --send; G-041 S2t). The goal form without --send stays in cmd_digest.go (S2s). The tick's morning_digest stays Python.

type digestAllCmd struct {
	goal, date, hours string
	send              bool
}

func parseDigestAll(rest []string) (any, bool) {
	c := digestAllCmd{hours: "24"}
	ok := flagParser{vals: map[string]*string{"--goal": &c.goal, "--date": &c.date, "--hours": &c.hours},
		bools: map[string]*bool{"--send": &c.send}}.parse(rest)
	return c, ok
}

func identDigestAll(p any) identity.Cmd {
	a, _ := p.(digestAllCmd)
	return identity.Cmd{Name: "digest", Send: a.send}
}

func runDigestAll(c *ctx, p any) (int, error) {
	a := p.(digestAllCmd)
	if a.send {
		if _, err := requireRole(c, "director"); err != nil {
			return 0, err
		}
	}
	hours, ok, err := pyIntStr(a.hours)
	if err != nil {
		return 0, err
	}
	if !ok || hours < 0 || hours > dgHoursMax {
		return 0, errDelegate // argparse's usage error, or arithmetic Python may refuse
	}
	var text string
	if a.goal != "" {
		text, err = dgBuild(c, a.goal, a.date, int(hours))
	} else {
		text, err = dgaBuild(c, a.date, int(hours))
	}
	if err != nil {
		return 0, err
	}
	if !a.send {
		c.out.WriteString(text)
		return 0, nil
	}
	day := a.date
	if day == "" {
		loc, err := sbLocation(c.env.Getenv)
		if err != nil {
			return 0, err
		}
		now, err := sbClock(c.env.Getenv, c.env.Now, loc)
		if err != nil {
			return 0, err
		}
		day = now.Format("2006-01-02")
	}
	var goal *string
	if a.goal != "" {
		goal = strp(a.goal)
	}
	mid, rcpt, err := msgSend(c, "boss", "daily digest "+day, strp(text), "report", goal, nil, "")
	if err != nil {
		return 0, err
	}
	c.out.WriteString("M" + strconv.FormatInt(mid, 10) + " digest → " + rcpt + "\n")
	return 0, nil
}

// dgaWindow is the (start, end] window of build_digest for date (or the clock) and hours, as dgBuild computes it.
func dgaWindow(c *ctx, date string, hours int) (dgWindow, error) {
	loc, err := sbLocation(c.env.Getenv)
	if err != nil {
		return dgWindow{}, err
	}
	var end time.Time
	if date != "" {
		d, err := time.Parse("2006-01-02", date)
		if err != nil || len(date) != 10 {
			return dgWindow{}, errDelegate // Python's strptime is more lenient, or dies with its own message
		}
		mid := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		_, o0 := mid.Zone()
		_, o1 := mid.Add(-12 * time.Hour).Zone()
		_, o2 := mid.Add(12 * time.Hour).Zone()
		if mid.Hour() != 0 || o0 != o1 || o0 != o2 {
			return dgWindow{}, errDelegate // a DST change near midnight
		}
		end = mid.In(time.FixedZone("", o0)).Add(24*time.Hour - time.Second)
	} else if end, err = sbClock(c.env.Getenv, c.env.Now, loc); err != nil {
		return dgWindow{}, err
	}
	if _, off := end.Zone(); off%60 != 0 {
		return dgWindow{}, errDelegate
	}
	return dgWindow{end.Add(-time.Duration(hours) * time.Hour), end, loc}, nil
}

// dgaBuild is build_digest(con, goal=None, date, hours): every goal, then the scout, retro, lessons and HQ sections.
func dgaBuild(c *ctx, date string, hours int) (string, error) {
	db := c.db
	w, err := dgaWindow(c, date, hours)
	if err != nil {
		return "", err
	}
	lc, err := sbLeadCounts(db)
	if err != nil {
		return "", err
	}
	title := date
	if title == "" {
		title = w.end.Format("2006-01-02")
	}
	row, err := sbDirectorRow(db, w.start, w.end, hours, w.loc)
	if err != nil {
		return "", err
	}
	out := []string{"# Factory digest " + title, "",
		fmt.Sprintf("Leads: %d live, %d parked, %d queued; workers live %d", lc.Leads, lc.Parked, lc.Queued, lc.Workers), "",
		"## Director", "", sbDirectorLine(row), ""}

	goals, err := dgaGoals(db)
	if err != nil {
		return "", err
	}
	events, err := dgaEvents(db, w)
	if err != nil {
		return "", err
	}
	scoreRows, err := sbScoreRows(db)
	if err != nil {
		return "", err
	}
	var closedEarlier []string
	var shown []dgGoal
	for _, g := range goals {
		if g.status == "done" || g.status == "cancelled" {
			closedRaw, err := dgGoalClosedAt(db, g.id, g.updated)
			if err != nil {
				return "", err
			}
			closed, ok, err := sbParseTS(closedRaw, w.loc)
			if err != nil {
				return "", err
			}
			if !ok || closed.Before(w.end.Add(-24*time.Hour)) || closed.After(w.end) { // a fixed 24 h, whatever --hours
				if ok && closed.After(w.start) && !closed.After(w.end) {
					closedEarlier = append(closedEarlier, fmt.Sprintf("- %s — %s (%s, closed %s)", g.id, g.title, g.status, closedRaw))
				}
				continue
			}
		}
		shown = append(shown, g)
	}
	counts := map[string]int{}
	for _, g := range shown {
		counts[g.org]++
	}
	out = append(out, fmt.Sprintf("Orgs: claude %d active · cursor %d active", counts["claude"], counts["cursor"]), "")
	enabled, err := dgEnabledOrgs(db)
	if err != nil {
		return "", err
	}
	freshShown := false
	for _, o := range dgOrgNames {
		var orgGoals []dgGoal
		for _, g := range shown {
			if g.org == o {
				orgGoals = append(orgGoals, g)
			}
		}
		if !enabled[o] && len(orgGoals) == 0 {
			continue
		}
		out = append(out, "## Org "+o, "")
		if len(orgGoals) == 0 {
			out = append(out, "_no active goals_", "")
			continue
		}
		for _, g := range orgGoals {
			block, err := dgGoalBlock(c, w, g, events, scoreRows, !freshShown)
			if err != nil {
				return "", err
			}
			freshShown = true
			out = append(out, block...)
		}
	}
	if !freshShown {
		f, err := dgFreshness(db)
		if err != nil {
			return "", err
		}
		out = append(out, f, "")
	}
	if len(closedEarlier) > 0 {
		out = append(out, "## Closed earlier", "")
		out = append(out, closedEarlier...)
		out = append(out, "")
	}
	scout, err := dgaScoutSection(db, w)
	if err != nil {
		return "", err
	}
	retro, err := dgaRetroSection(db, w)
	if err != nil {
		return "", err
	}
	out = append(out, scout, "", retro, "")
	lessons, err := dgaLessons(db, w)
	if err != nil {
		return "", err
	}
	out = append(out, lessons...)
	hq, err := dgaHQ(db)
	if err != nil {
		return "", err
	}
	out = append(out, hq...)
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", nil
}

// dgaGoals is every goal in id order, its org resolved as goal_org(...) or DEFAULT_ORG.
func dgaGoals(db *sql.DB) ([]dgGoal, error) {
	rs, err := db.Query("SELECT id, title, status, org, updated FROM goals ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	var out []dgGoal
	for rs.Next() {
		var g dgGoal
		var org sql.NullString
		if err := rs.Scan(&g.id, &g.title, &g.status, &org, &g.updated); err != nil {
			return nil, errDelegate
		}
		g.org = dgGoalOrg(org.String)
		out = append(out, g)
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	return out, nil
}

// dgaEvents is the events of the window in id order (the same query dgBuild runs).
func dgaEvents(db *sql.DB, w dgWindow) ([]dgEvent, error) {
	rs, err := db.Query("SELECT id, ts, entity, entity_id, action, detail FROM events ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	var events []dgEvent
	for rs.Next() {
		var e dgEvent
		var ent, eid, det sql.NullString
		if err := rs.Scan(&e.id, &e.ts, &ent, &eid, &e.act, &det); err != nil {
			return nil, errDelegate
		}
		t, in, werr := w.in(e.ts)
		if werr != nil {
			return nil, werr
		}
		if !in {
			continue
		}
		e.t, e.entity, e.entityID, e.detail, e.hasEntity, e.hasEID = t, ent.String, eid.String, det.String, ent.Valid, eid.Valid
		events = append(events, e)
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	return events, nil
}
