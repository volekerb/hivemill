package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"factory/go/internal/schedule"
)

func init() {
	register("schedule", "add", handler{parse: parseScheduleAdd, run: runScheduleAdd})
	register("schedule", "list", handler{parse: parseScheduleList, run: runScheduleList})
	register("schedule", "remove", handler{parse: parseScheduleRemove, run: runScheduleRemove})
}

type scheduleAddCmd struct {
	hour, days, goal, tokenCap, job string
}

type scheduleListCmd struct {
	all, json bool
}

type scheduleRemoveCmd struct {
	id string
}

func parseScheduleAdd(rest []string) (any, bool) {
	var c scheduleAddCmd
	var pos []string
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "--") {
			if a == "" || a[0] == '-' {
				return nil, false
			}
			pos = append(pos, a)
			continue
		}
		if seen[a] {
			return nil, false
		}
		seen[a] = true
		if i+1 >= len(rest) || rest[i+1] == "" || rest[i+1][0] == '-' {
			return nil, false
		}
		i++
		switch a {
		case "--hour":
			c.hour = rest[i]
		case "--days":
			c.days = rest[i]
		case "--goal":
			c.goal = rest[i]
		case "--token-cap":
			c.tokenCap = rest[i]
		default:
			return nil, false
		}
	}
	if c.hour == "" || c.goal == "" || c.tokenCap == "" || len(pos) != 1 {
		return nil, false
	}
	c.job = pos[0]
	return c, true
}

func parseScheduleList(rest []string) (any, bool) {
	var c scheduleListCmd
	ok := flagParser{bools: map[string]*bool{"--all": &c.all, "--json": &c.json}}.parse(rest)
	return c, ok
}

func parseScheduleRemove(rest []string) (any, bool) {
	if len(rest) != 1 || rest[0] == "" || rest[0][0] == '-' {
		return nil, false
	}
	return scheduleRemoveCmd{id: rest[0]}, true
}

func scheduleRequireBoss(c *ctx, entityID, detail string) error {
	if _, err := requireRole(c, "boss"); err != nil {
		if e := event(c, "schedule", entityID, "schedule-refused", &detail); e != nil {
			return e
		}
		return err
	}
	return nil
}

func schedExit2(err error) (int, error) {
	var ve *schedule.ValidationError
	if errors.As(err, &ve) {
		return 0, dieErr(2, ve.Msg)
	}
	return 0, err
}

func runScheduleAdd(c *ctx, p any) (int, error) {
	a := p.(scheduleAddCmd)
	if err := scheduleRequireBoss(c, "", "schedule add"); err != nil {
		return 0, err
	}
	hour, err := schedule.ParseHour(a.hour)
	if err != nil {
		return schedExit2(err)
	}
	cap, err := schedule.ParseTokenCap(a.tokenCap)
	if err != nil {
		return schedExit2(err)
	}
	digest, err := cfgText(c, "digest_hour")
	if err != nil {
		return 0, err
	}
	retro, err := cfgText(c, "retro_hour")
	if err != nil {
		return 0, err
	}
	slots, err := cfgText(c, "scout_slots")
	if err != nil {
		return 0, err
	}
	if w := schedule.HourOverlapWarning(hour, digest, retro, slots); w != "" {
		fmt.Fprintln(c.errb, w)
	}
	id, err := schedule.Add(c.db, c.env.Now(), c.who, hour, a.days, a.goal, a.job, cap)
	if err != nil {
		return schedExit2(err)
	}
	fmt.Fprintln(c.out, id)
	return 0, nil
}

func runScheduleRemove(c *ctx, p any) (int, error) {
	a := p.(scheduleRemoveCmd)
	if err := scheduleRequireBoss(c, a.id, "schedule remove"); err != nil {
		return 0, err
	}
	err := schedule.Remove(c.db, c.env.Now(), c.who, a.id)
	if err != nil {
		return schedExit2(err)
	}
	return 0, nil
}

var scheduleListCols = []string{"id", "hour", "days", "goal", "cap", "last_fired", "outcome", "job"}

func runScheduleList(c *ctx, p any) (int, error) {
	a := p.(scheduleListCmd)
	rows, err := schedule.List(c.db, !a.all)
	if err != nil {
		return 0, err
	}
	digest, err := cfgText(c, "digest_hour")
	if err != nil {
		return 0, err
	}
	retro, err := cfgText(c, "retro_hour")
	if err != nil {
		return 0, err
	}
	slots, err := cfgText(c, "scout_slots")
	if err != nil {
		return 0, err
	}
	if a.json {
		type rowJSON struct {
			ID            string  `json:"id"`
			Hour          string  `json:"hour"`
			Days          string  `json:"days"`
			GoalID        string  `json:"goal_id"`
			TokenCap      int     `json:"token_cap"`
			Job           string  `json:"job"`
			CreatedBy     string  `json:"created_by"`
			Created       string  `json:"created"`
			RemovedAt     *string `json:"removed_at"`
			LastFired     *string `json:"last_fired_date"`
			LastOutcome   *string `json:"last_outcome"`
			LastOutcomeAt *string `json:"last_outcome_at"`
		}
		var out []rowJSON
		for _, r := range rows {
			row := rowJSON{ID: r.ID, Hour: fmt.Sprintf("%02d", r.Hour), Days: r.Days, GoalID: r.GoalID,
				TokenCap: r.TokenCap, Job: r.Job, CreatedBy: r.CreatedBy, Created: r.Created,
				RemovedAt: nullPtr(r.RemovedAt), LastFired: nullPtr(r.LastFiredDate), LastOutcome: nullPtr(r.LastOutcome),
				LastOutcomeAt: nullPtr(r.LastOutcomeAt)}
			out = append(out, row)
		}
		b, err := json.MarshalIndent(out, "", " ")
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(c.out, string(b))
	} else {
		var table [][]string
		for _, r := range rows {
			table = append(table, []string{r.ID, fmt.Sprintf("%02d", r.Hour), r.Days, r.GoalID, strconv.Itoa(r.TokenCap),
				schedCell(r.LastFiredDate), schedCell(r.LastOutcome), r.Job})
		}
		Table(c.out, scheduleListCols, table)
	}
	fmt.Fprintf(c.out, "fixed schedules: digest_hour=%s retro_hour=%s scout_slots=%s\n", digest, retro, slots)
	return 0, nil
}

func schedCell(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}

func nullPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}
