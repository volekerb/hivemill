// Package prflow measures how pull requests flow through the factory pipeline: merges per day, open-to-merge time and
// where each PR spent its hours (CI wait, review and QA, rework, judge, hold, refresh, red main). It reads GitHub
// through two `gh pr list` calls and the factory event log (state/events.jsonl), and writes nothing.
package prflow

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/go/internal/ghx"
)

// ghLimit is the --limit of each gh pr list call.
const ghLimit = 500

// Orgs is the stable org order of every output.
var Orgs = []string{"claude", "cursor", "other"}

// runGH is the only way this package calls gh. Tests replace it.
var runGH = func(args ...string) ([]byte, error) {
	r, err := ghx.Run(nil, "", 120*time.Second, args...)
	if err != nil {
		return nil, fmt.Errorf("run gh: %w", err)
	}
	if r.ExitCode != 0 {
		line, _, _ := strings.Cut(strings.TrimSpace(r.Stderr), "\n")
		return nil, fmt.Errorf("gh %s exited %d: %s (check `gh auth status` and the --repo name)",
			strings.Join(args[:min(2, len(args))], " "), r.ExitCode, line)
	}
	return []byte(r.Stdout), nil
}

// PR is one row of gh pr list --json.
type PR struct {
	Number           int        `json:"number"`
	State            string     `json:"state"`
	CreatedAt        time.Time  `json:"createdAt"`
	MergedAt         *time.Time `json:"mergedAt"`
	HeadRefName      string     `json:"headRefName"`
	MergeStateStatus string     `json:"mergeStateStatus"`
}

// Options are the inputs of one run.
type Options struct {
	Repo   string
	Since  time.Duration
	Now    time.Time
	Events io.Reader
	// RedMainAll adds the red-main numbers of every repo in the event log to the report.
	RedMainAll bool
}

// DayRow is the merge count of one UTC day.
type DayRow struct {
	Day    string `json:"day"`
	All    int    `json:"all"`
	Claude int    `json:"claude"`
	Cursor int    `json:"cursor"`
	Other  int    `json:"other"`
}

// MergeStats describes the open-to-merge hours of the PRs merged in the window.
type MergeStats struct {
	Org     string  `json:"org"`
	N       int     `json:"n"`
	Median  float64 `json:"median_h"`
	P90     float64 `json:"p90_h"`
	Max     float64 `json:"max_h"`
	Over6h  int     `json:"over_6h"`
	sampleH []float64
}

// OpenPR is an open PR older than 4 hours.
type OpenPR struct {
	Number           int     `json:"number"`
	AgeHours         float64 `json:"age_h"`
	MergeStateStatus string  `json:"merge_state_status"`
	Stage            string  `json:"stage"`
	Org              string  `json:"org"`
	Task             string  `json:"task"`
}

// StageRow is the hours all PRs of the report spent in one stage.
type StageRow struct {
	Stage  string  `json:"stage"`
	All    float64 `json:"all_h"`
	Claude float64 `json:"claude_h"`
	Cursor float64 `json:"cursor_h"`
	Other  float64 `json:"other_h"`
}

// SlowPR is one of the slowest PRs with the stage that cost it the most.
type SlowPR struct {
	Number       int     `json:"number"`
	State        string  `json:"state"`
	Hours        float64 `json:"hours"`
	Org          string  `json:"org"`
	Task         string  `json:"task"`
	LargestStage string  `json:"largest_stage"`
	LargestHours float64 `json:"largest_stage_h"`
}

// Report is the whole result. Text and JSON output print the same numbers.
type Report struct {
	Repo          string       `json:"repo"`
	Since         string       `json:"since"`
	Now           string       `json:"now"`
	WindowHours   float64      `json:"window_h"`
	PRs           int          `json:"prs"`
	EventLines    int          `json:"event_lines"`
	EventsSkipped int          `json:"event_lines_skipped"`
	MergedPerDay  []DayRow     `json:"merged_per_day"`
	OpenToMerge   []MergeStats `json:"open_to_merge"`
	OpenOver4h    []OpenPR     `json:"open_over_4h"`
	OpenOver6h    []OpenPR     `json:"open_over_6h"`
	StageHours    []StageRow   `json:"stage_hours"`
	ReviewerFail  int          `json:"reviewer_fail"`
	QAFail        int          `json:"qa_fail"`
	MultiRoundPRs int          `json:"prs_with_2plus_review_rounds"`
	Refreshes     int          `json:"refreshes"`
	Slowest       []SlowPR     `json:"slowest"`
	Warnings      []string     `json:"warnings"`
	// RedMain is the red-main section of the repo; RedMainRepos (--red-main-all) lists every repo. Keys added by
	// T-1760: the keys above are unchanged.
	RedMain      RedMain   `json:"red_main"`
	RedMainRepos []RedMain `json:"red_main_repos,omitempty"`
}

const prFields = "number,state,createdAt,mergedAt,headRefName,mergeStateStatus"

var branchTaskRe = regexp.MustCompile(`^factory/[tT]-(\d+)$`)

// FetchPRs lists the PRs of the report with two gh calls: the PRs created in the window, and the PRs created before it
// that were not closed before it (merged in the window, or still open). Closed, unmerged PRs are dropped.
func FetchPRs(repo string, since time.Time) (prs []PR, warnings []string, err error) {
	cut := since.UTC().Format("2006-01-02T15:04:05Z")
	seen := map[int]bool{}
	for _, search := range []string{"created:>=" + cut, "created:<" + cut + " -closed:<" + cut} {
		out, gerr := runGH("pr", "list", "-R", repo, "--state", "all", "--limit", fmt.Sprint(ghLimit),
			"--search", search, "--json", prFields)
		if gerr != nil {
			return nil, nil, gerr
		}
		var rows []PR
		if jerr := json.Unmarshal(out, &rows); jerr != nil {
			return nil, nil, fmt.Errorf("gh pr list --search %q: output is not a JSON list: %w", search, jerr)
		}
		if len(rows) >= ghLimit {
			warnings = append(warnings, fmt.Sprintf("gh pr list --search %q returned %d rows (the limit): "+
				"the report misses PRs; use a shorter --since", search, len(rows)))
		}
		for _, p := range rows {
			if seen[p.Number] || p.Number <= 0 || p.CreatedAt.IsZero() {
				continue
			}
			seen[p.Number] = true
			if p.State == "CLOSED" || (p.State == "MERGED" && p.MergedAt == nil) {
				continue
			}
			prs = append(prs, p)
		}
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })
	return prs, warnings, nil
}

// taskOf resolves a PR to its task: the update event with pr=<url>, else the pr-create event of its head branch, else
// a factory/t-NNNN branch name. "" means no task.
func (ix *Index) taskOf(p PR) string {
	if t, ok := ix.PRTask[p.Number]; ok {
		return t
	}
	if t, ok := ix.BranchTask[p.HeadRefName]; ok && p.HeadRefName != "" {
		return t
	}
	if m := branchTaskRe.FindStringSubmatch(p.HeadRefName); m != nil {
		return "T-" + m[1]
	}
	return ""
}

func (ix *Index) orgOf(task string) string {
	if o := ix.TaskOrg[task]; o != "" {
		return o
	}
	return "other"
}

// Run builds the report.
func Run(o Options) (*Report, error) {
	if o.Repo == "" || strings.Count(o.Repo, "/") != 1 {
		return nil, fmt.Errorf("--repo %q: want owner/name", o.Repo)
	}
	if o.Since <= 0 {
		return nil, fmt.Errorf("--since must be positive, got %s", o.Since)
	}
	now := o.Now.UTC()
	since := now.Add(-o.Since)
	prs, warnings, err := FetchPRs(o.Repo, since)
	if err != nil {
		return nil, err
	}
	notBefore := since
	for _, p := range prs {
		if p.CreatedAt.Before(notBefore) {
			notBefore = p.CreatedAt
		}
	}
	ix, err := ReadEvents(o.Events, o.Repo, notBefore)
	if err != nil {
		return nil, err
	}

	rep := &Report{
		Repo: o.Repo, Since: stamp(since), Now: stamp(now), WindowHours: round2(o.Since.Hours()),
		PRs: len(prs), EventLines: ix.Lines, EventsSkipped: ix.Skipped,
		OpenOver4h: []OpenPR{}, OpenOver6h: []OpenPR{}, Slowest: []SlowPR{}, Warnings: append([]string{}, warnings...),
	}
	rep.RedMain = redMainOf(ix.RedRepos[o.Repo], since, now)
	rep.RedMain.setRate(o.Since)
	if o.RedMainAll {
		repos := make([]string, 0, len(ix.RedRepos))
		for k := range ix.RedRepos {
			repos = append(repos, k)
		}
		sort.Strings(repos)
		for _, k := range repos {
			rm := redMainOf(ix.RedRepos[k], since, now)
			rm.Repo = k
			rm.setRate(o.Since)
			rep.RedMainRepos = append(rep.RedMainRepos, rm)
		}
	}
	days := map[string]*DayRow{}
	for d := since.Truncate(24 * time.Hour); !d.After(now); d = d.Add(24 * time.Hour) {
		row := &DayRow{Day: d.Format("2006-01-02")}
		days[row.Day] = row
	}
	stats := map[string]*MergeStats{"all": {Org: "all"}}
	stage := map[string]*StageRow{}
	for _, s := range StageOrder {
		stage[s] = &StageRow{Stage: s}
	}
	for _, org := range Orgs {
		stats[org] = &MergeStats{Org: org}
	}

	for _, p := range prs {
		task := ix.taskOf(p)
		org := ix.orgOf(task)
		merged := p.MergedAt != nil
		end := now
		if merged {
			end = p.MergedAt.UTC()
		}
		if merged && end.Before(since) {
			continue
		}
		w := Stages(p.CreatedAt, end, ix.Events[task], ix.Red)
		hours := end.Sub(p.CreatedAt).Hours()
		rep.ReviewerFail += w.ReviewerFail
		rep.QAFail += w.QAFail
		rep.Refreshes += w.Refreshes
		if w.Rounds >= 2 {
			rep.MultiRoundPRs++
		}
		largest, largestH := StageCIWait, 0.0
		wh := w.Hours()
		for _, s := range StageOrder {
			h := wh[s]
			row := stage[s]
			row.All += h
			switch org {
			case "claude":
				row.Claude += h
			case "cursor":
				row.Cursor += h
			default:
				row.Other += h
			}
			if h > largestH {
				largest, largestH = s, h
			}
		}
		rep.Slowest = append(rep.Slowest, SlowPR{
			Number: p.Number, State: p.State, Hours: round2(hours), Org: org, Task: task,
			LargestStage: largest, LargestHours: round2(largestH),
		})
		if merged {
			if row := days[end.Format("2006-01-02")]; row != nil {
				row.All++
				switch org {
				case "claude":
					row.Claude++
				case "cursor":
					row.Cursor++
				default:
					row.Other++
				}
			}
			for _, k := range []string{"all", org} {
				stats[k].sampleH = append(stats[k].sampleH, hours)
			}
			continue
		}
		if hours > 4 {
			op := OpenPR{
				Number: p.Number, AgeHours: round2(hours), MergeStateStatus: p.MergeStateStatus,
				Stage: w.Current(), Org: org, Task: task,
			}
			rep.OpenOver4h = append(rep.OpenOver4h, op)
			if hours > 6 {
				rep.OpenOver6h = append(rep.OpenOver6h, op)
			}
		}
	}

	dayKeys := make([]string, 0, len(days))
	for k := range days {
		dayKeys = append(dayKeys, k)
	}
	sort.Strings(dayKeys)
	for _, k := range dayKeys {
		rep.MergedPerDay = append(rep.MergedPerDay, *days[k])
	}
	for _, k := range append([]string{"all"}, Orgs...) {
		s := stats[k]
		sort.Float64s(s.sampleH)
		s.N = len(s.sampleH)
		if s.N > 0 {
			s.Median = round2(percentile(s.sampleH, 0.5))
			s.P90 = round2(percentile(s.sampleH, 0.9))
			s.Max = round2(s.sampleH[s.N-1])
		}
		for _, h := range s.sampleH {
			if h > 6 {
				s.Over6h++
			}
		}
		rep.OpenToMerge = append(rep.OpenToMerge, *s)
	}
	for _, s := range StageOrder {
		row := *stage[s]
		row.All, row.Claude, row.Cursor, row.Other = round2(row.All), round2(row.Claude), round2(row.Cursor), round2(row.Other)
		rep.StageHours = append(rep.StageHours, row)
	}
	byAge := func(a, b OpenPR) bool {
		if a.AgeHours != b.AgeHours {
			return a.AgeHours > b.AgeHours
		}
		return a.Number < b.Number
	}
	sort.Slice(rep.OpenOver4h, func(i, j int) bool { return byAge(rep.OpenOver4h[i], rep.OpenOver4h[j]) })
	sort.Slice(rep.OpenOver6h, func(i, j int) bool { return byAge(rep.OpenOver6h[i], rep.OpenOver6h[j]) })
	sort.Slice(rep.Slowest, func(i, j int) bool {
		a, b := rep.Slowest[i], rep.Slowest[j]
		if a.Hours != b.Hours {
			return a.Hours > b.Hours
		}
		return a.Number < b.Number
	})
	if len(rep.Slowest) > 10 {
		rep.Slowest = rep.Slowest[:10]
	}
	return rep, nil
}

// percentile is the linear-interpolation percentile of a sorted, non-empty sample (q in 0..1).
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// JSON renders the report as one indented JSON object.
func (r *Report) JSON() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode report: %w", err)
	}
	return string(b) + "\n", nil
}

// Text renders the report for a human; the order of lines is stable.
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pr-flow %s: window %s .. %s (%.0f h), %d PRs, %d event lines (%d skipped)\n",
		r.Repo, r.Since, r.Now, r.WindowHours, r.PRs, r.EventLines, r.EventsSkipped)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "merged per day (UTC) %10s %7s %7s %7s\n", "all", "claude", "cursor", "other")
	for _, d := range r.MergedPerDay {
		fmt.Fprintf(&b, "  %-18s %10d %7d %7d %7d\n", d.Day, d.All, d.Claude, d.Cursor, d.Other)
	}
	fmt.Fprintf(&b, "open-to-merge hours  %10s %7s %7s %7s %7s\n", "n", "median", "p90", "max", "over6h")
	for _, s := range r.OpenToMerge {
		fmt.Fprintf(&b, "  %-18s %10d %7.1f %7.1f %7.1f %7d\n", s.Org, s.N, s.Median, s.P90, s.Max, s.Over6h)
	}
	for _, set := range []struct {
		name string
		prs  []OpenPR
	}{{"open over 4 h", r.OpenOver4h}, {"open over 6 h", r.OpenOver6h}} {
		fmt.Fprintf(&b, "%s: %d\n", set.name, len(set.prs))
		for _, p := range set.prs {
			fmt.Fprintf(&b, "  #%-5d age %6.1f h  %-10s stage %-14s %-6s %s\n",
				p.Number, p.AgeHours, dash(p.MergeStateStatus), p.Stage, p.Org, dash(p.Task))
		}
	}
	fmt.Fprintf(&b, "stage hours          %10s %7s %7s %7s\n", "all", "claude", "cursor", "other")
	for _, s := range r.StageHours {
		fmt.Fprintf(&b, "  %-18s %10.1f %7.1f %7.1f %7.1f\n", s.Stage, s.All, s.Claude, s.Cursor, s.Other)
	}
	fmt.Fprintf(&b, "review: reviewer FAIL %d, QA FAIL %d, PRs with 2+ review rounds %d, refreshes %d\n",
		r.ReviewerFail, r.QAFail, r.MultiRoundPRs, r.Refreshes)
	fmt.Fprintf(&b, "slowest PRs (top %d)\n", len(r.Slowest))
	for _, p := range r.Slowest {
		fmt.Fprintf(&b, "  #%-5d %6.1f h  %-6s %-6s %-7s largest stage %s %.1f h\n",
			p.Number, p.Hours, p.State, p.Org, dash(p.Task), p.LargestStage, p.LargestHours)
	}
	fmt.Fprintf(&b, "red main (%s, days by the ts's own UTC offset)\n", r.Repo)
	r.RedMain.text(&b, "  ")
	for _, rm := range r.RedMainRepos {
		fmt.Fprintf(&b, "red main repo %s\n", rm.Repo)
		rm.text(&b, "  ")
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// OpenEvents opens the events file. path "" means $FACTORY_HOME/state/events.jsonl.
func OpenEvents(path string) (*os.File, error) {
	if path == "" {
		home := os.Getenv("FACTORY_HOME")
		if home == "" {
			return nil, fmt.Errorf("FACTORY_HOME is not set: pass --events <path to events.jsonl>")
		}
		path = home + "/state/events.jsonl"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open events file (pass --events <path> if it lives elsewhere): %w", err)
	}
	return f, nil
}

// ParseSince reads a window such as 72h, 90m or 3d.
func ParseSince(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(days, "%d", &n); err == nil && fmt.Sprint(n) == days && n > 0 && n <= 3650 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
		return 0, fmt.Errorf("--since %q: want a duration such as 72h or 3d", s)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q: want a positive duration such as 72h or 3d", s)
	}
	return d, nil
}
