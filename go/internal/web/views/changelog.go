package views

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"factory/go/internal/web/data"
)

const titleChangelog = "Changelog"

var changelogGoalRe = regexp.MustCompile(`^G-\p{Nd}+\n?\z`) // ^G-\d+$ with Python's Unicode \d and a $ that matches before a final newline

// ChangelogGoal is one goal chip of the changelog filter.
type ChangelogGoal struct {
	GoalID    string
	GoalTitle *string
	Count     int
}

// ChangelogData is what the Changelog page shows (web/views/changelog.py data()).
type ChangelogData struct {
	Org, Goal, Group string
	Goals            []ChangelogGoal
	Groups           []data.ChangeGroup
	Total            int
}

func groupEpoch(g data.ChangeGroup) float64 {
	best := 0.0
	for _, i := range g.Items {
		if e := epochOf(i.Merged); e > best {
			best = e
		}
	}
	return best
}

// LoadChangelog gathers the data of the Changelog page.
func LoadChangelog(ctx *Ctx) (*ChangelogData, error) {
	goals, org, err := goalFilter(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := ctx.DB.Changelog(goals)
	if err != nil {
		return nil, fmt.Errorf("views: changelog: %w", err)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groupEpoch(groups[i]) > groupEpoch(groups[j]) })
	d := &ChangelogData{Org: org, Group: "date"}
	if ctx.Query["group"] == "goal" {
		d.Group = "goal"
	}
	want := ctx.Query["goal"]
	if changelogGoalRe.MatchString(want) {
		for _, g := range groups {
			if g.GoalID == want {
				d.Goal = want
			}
		}
	}
	for _, g := range groups {
		d.Goals = append(d.Goals, ChangelogGoal{g.GoalID, g.GoalTitle, len(g.Items)})
		if d.Goal == "" || g.GoalID == d.Goal {
			d.Groups = append(d.Groups, g)
			d.Total += len(g.Items)
		}
	}
	return d, nil
}

// dayLabel is changelog._day: "Today", "Yesterday" or "Nov 03" in local time; "Unknown date" when unparseable.
func dayLabel(iso string, now time.Time) string {
	t, ok := data.ParseTS(iso)
	if !ok {
		return "Unknown date"
	}
	a, b := now.In(data.Loc), t.In(data.Loc)
	da := time.Date(a.Year(), a.Month(), a.Day(), 12, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 12, 0, 0, 0, time.UTC)
	switch int(da.Sub(db).Hours() / 24) {
	case 0:
		return "Today"
	case 1:
		return "Yesterday"
	}
	return b.Format("Jan 02")
}

func changelogRow(i data.ChangeItem, goalID string, now time.Time) any {
	title := i.Title
	if title == "" {
		title = i.TaskID
	}
	return FeedRow(title, "/task/"+i.TaskID, goalID, deref(i.PRURL), deref(i.SHA), Ago(i.Merged, now, " ago"), i.Merged)
}

// RenderChangelog is changelog.render.
func RenderChangelog(ctx *Ctx, d *ChangelogData) Safe {
	if len(d.Groups) == 0 {
		return Section("Shipped", nil, EmptyState("Nothing has been merged yet.", nil))
	}
	now, goal, mode := ctx.Now, d.Goal, d.Group
	lineNote := El("p", "Entries use a bounded line length on desktop so titles stay readable; meta stays near the title.", Cls("muted hide-phone changelog-line-note"))
	groupHref := func(extra ...KV) string { return OrgHref("/changelog", d.Org, extra...) }
	seg := Segmented("Group changelog", []SegItem{
		{"By date", groupHref(KV{"goal", goal}), mode == "date"},
		{"By goal", groupHref(KV{"goal", goal}, KV{"group", "goal"}), mode == "goal"},
	}, false)
	parts := []any{El("div", lineNote, seg, Cls("changelog-meta"))}
	if len(d.Goals) > 1 || goal != "" {
		keep := []KV{}
		if mode == "goal" {
			keep = append(keep, KV{"group", "goal"})
		}
		items := []Chip{{"All goals", nil, groupHref(keep...), goal == ""}}
		for _, g := range d.Goals {
			if g.GoalID != "" {
				items = append(items, Chip{g.GoalID, N(g.Count), groupHref(append([]KV{{"goal", g.GoalID}}, keep...)...), goal == g.GoalID})
			}
		}
		active := 0
		if goal != "" {
			active = 1
		}
		parts = append(parts, FilterBar("changelog-filters", active, nil, Chips("Goal", items)))
	}
	if mode == "goal" {
		for _, g := range d.Groups {
			title := "No goal"
			if g.GoalID != "" {
				title = g.GoalID + " - " + deref(g.GoalTitle)
			}
			var rows []any
			for _, i := range g.Items {
				rows = append(rows, changelogRow(i, "", now))
			}
			parts = append(parts, Section(title, len(g.Items), Feed(rows, "log")))
		}
	} else {
		type pair struct {
			i data.ChangeItem
			g string
		}
		var items []pair
		for _, g := range d.Groups {
			for _, i := range g.Items {
				items = append(items, pair{i, g.GoalID})
			}
		}
		sort.SliceStable(items, func(a, b int) bool {
			ea, eb := epochOf(items[a].i.Merged), epochOf(items[b].i.Merged)
			if ea != eb {
				return ea > eb
			}
			return items[a].i.TaskID > items[b].i.TaskID
		})
		var order []string
		days := map[string][]any{}
		for _, p := range items {
			day := dayLabel(p.i.Merged, now)
			if _, ok := days[day]; !ok {
				order = append(order, day)
			}
			days[day] = append(days[day], changelogRow(p.i, p.g, now))
		}
		for _, day := range order {
			parts = append(parts, Section(day, len(days[day]), Feed(days[day], "log")))
		}
	}
	return El("div", parts...)
}

type changelogView struct{}

// Render implements View.
func (changelogView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadChangelog(ctx)
	if err != nil {
		return "", "", err
	}
	return titleChangelog, RenderChangelog(ctx, d), nil
}
