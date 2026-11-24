package digest

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/store"
)

// Env is what the digest reads from the process: TZ and FACTORY_NOW through Getenv, the real clock through Now (nil is
// time.Now). The tick and the cli each pass their own, so tests pin both.
type Env struct {
	Getenv func(string) string
	Now    func() time.Time
}

func (e Env) now() func() time.Time {
	if e.Now == nil {
		return time.Now
	}
	return e.Now
}

// HoursMax bounds --hours so the window arithmetic cannot overflow where Python's would raise.
const HoursMax = 100000

// Build is build_digest(con, goal, date, hours): the goal digest for goal != "", else the factory-wide one. date ""
// means the clock's day. An input the port cannot reproduce byte for byte returns identity.ErrDelegate.
func Build(s *store.Store, env Env, goal, date string, hours int) (string, error) {
	if goal != "" {
		return dgBuild(s, env, goal, date, hours)
	}
	return dgaBuild(s, env, date, hours)
}

// Day is the date a digest message is named after: date, else the clock's day.
func Day(env Env, date string) (string, error) {
	if date != "" {
		return date, nil
	}
	loc, err := sbLocation(env.Getenv)
	if err != nil {
		return "", err
	}
	now, err := sbClock(env.Getenv, env.now(), loc)
	if err != nil {
		return "", err
	}
	return now.Format("2006-01-02"), nil
}

// Send is send(con, "boss", "daily digest <day>", text, "report", goal, sender=sender, org=org): the store-only send of the
// tick. goal "" stores NULL. It returns the message id and the resolved recipient.
func Send(s *store.Store, day, text, goal, sender, org string) (int64, string, error) {
	m := store.Msg{To: "boss", Subject: "daily digest " + day, Body: &text, Kind: "report", Sender: sender, Org: org}
	if goal != "" {
		m.Goal = &goal
	}
	id, rcpt, err := s.Send(m)
	if err != nil {
		return 0, "", fmt.Errorf("send the digest: %w", err)
	}
	return id, rcpt, nil
}

// Line is the "M<id> digest → <recipient>" line `digest --send` prints.
func Line(id int64, rcpt string) string {
	return "M" + strconv.FormatInt(id, 10) + " digest → " + rcpt + "\n"
}

// Exports for the cli commands that share the scoreboard helpers (scorecard, res).
var (
	ParseTS       = sbParseTS
	Location      = sbLocation
	Clock         = sbClock
	ISO           = sbISO
	Count         = sbCount
	CountLeads    = sbLeadCounts
	ScoreRows     = sbScoreRows
	DirectorRowAt = sbDirectorRow
	DirectorLine  = sbDirectorLine
	PyIntStr      = pyIntStr
	OwnerCapKeys  = &sbOwnerCapKeys
	WorkerRoles   = &sbWorkerRoles
)

// EnabledOrgs is enabled_orgs(con) in the order of the config value (duplicates and unknown names dropped; the default
// org alone when none is left).
func EnabledOrgs(db *sql.DB) ([]string, error) {
	raw, err := sbCfg(db, "orgs_enabled")
	if err != nil {
		return nil, err
	}
	var s string
	switch x := raw.(type) {
	case nil:
	case string:
		s = x
	default:
		return nil, errDelegate
	}
	var out []string
	seen := map[string]bool{}
	for _, o := range strings.Split(s, ",") {
		if o = store.PyStrip(o); dgIsOrg(o) && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		out = append(out, dgDefaultOrg)
	}
	return out, nil
}
