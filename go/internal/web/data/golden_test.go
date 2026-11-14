package data

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	defaults = []Default{{"max_live_agents", Int(6)}, {"max_five_hour_pct", Int(92)}, {"max_seven_day_pct", Int(92)},
		{"min_mem_available_mb", Int(1024)}, {"ratio", Float(0.5)}}
	goalsG1   = GoalSet{"G-001": true}
	allTasks  = []string{"T-1", "T-2", "T-3", "T-5", "T-6", "T-9", "T-11", "T-12", "T-55", "T-99"}
	usageOK   = &Usage{FiveHour: fp(50), SevenDay: fp(10), Fresh: true, AgeMin: 1}
	stubShell = func(c *Conn, now time.Time) (any, error) { return map[string]bool{"stub": true}, nil }
)

func fp(f float64) *float64 { return &f }

type goldenCase struct {
	name string
	run  func(c *Conn) (any, error)
}

// wrap adapts a (T, error) method to the case signature.
func wrap[T any](v T, err error) (any, error) { return v, err }

func goldenCases() []goldenCase {
	later := testNow.Add(time.Hour)
	return []goldenCase{
		{"goals", func(c *Conn) (any, error) { return wrap(c.Goals()) }},
		{"goal_detail_g001", func(c *Conn) (any, error) { return wrap(c.GoalDetail("G-001")) }},
		{"goal_detail_g002", func(c *Conn) (any, error) { return wrap(c.GoalDetail("G-002")) }},
		{"goal_detail_g003", func(c *Conn) (any, error) { return wrap(c.GoalDetail("G-003")) }},
		{"goal_detail_missing", func(c *Conn) (any, error) { return wrap(c.GoalDetail("G-999")) }},
		{"internal_blocked", func(c *Conn) (any, error) { return wrap(c.InternalBlocked()) }},
		{"owner_pending", func(c *Conn) (any, error) { return wrap(c.OwnerPending()) }},
		{"teams", func(c *Conn) (any, error) { return wrap(c.Teams()) }},
		{"goal_teams", func(c *Conn) (any, error) { return wrap(c.GoalTeams(testNow)) }},
		{"agents", func(c *Conn) (any, error) { return wrap(c.Agents(false, testNow)) }},
		{"agents_all", func(c *Conn) (any, error) { return wrap(c.Agents(true, testNow)) }},
		{"utilization_fresh", func(c *Conn) (any, error) { return wrap(c.UtilizationSample(testNow)) }},
		{"utilization_stale", func(c *Conn) (any, error) { return wrap(c.UtilizationSample(later)) }},
		{"tasks", func(c *Conn) (any, error) { return wrap(c.Tasks("", "")) }},
		{"tasks_goal", func(c *Conn) (any, error) { return wrap(c.Tasks("G-001", "")) }},
		{"tasks_status", func(c *Conn) (any, error) { return wrap(c.Tasks("", "done")) }},
		{"tasks_goal_status", func(c *Conn) (any, error) { return wrap(c.Tasks("G-002", "todo")) }},
		{"pr_cycle_t1", func(c *Conn) (any, error) { return wrap(c.PRCycle("T-1")) }},
		{"pr_cycle_t2", func(c *Conn) (any, error) { return wrap(c.PRCycle("T-2")) }},
		{"pr_cycle_t5", func(c *Conn) (any, error) { return wrap(c.PRCycle("T-5")) }},
		{"pr_cycle_t9", func(c *Conn) (any, error) { return wrap(c.PRCycle("T-9")) }},
		{"pr_cycle_none", func(c *Conn) (any, error) { return wrap(c.PRCycle("T-404")) }},
		{"events", func(c *Conn) (any, error) { return wrap(c.Events("", nil, "", 500)) }},
		{"events_filtered", func(c *Conn) (any, error) {
			return wrap(c.Events("2026-11-12T00:00:00+01:00", []string{"merged", "update", "judge-reject"}, "", 7))
		}},
		{"events_entity", func(c *Conn) (any, error) { return wrap(c.Events("", nil, "agent", 3)) }},
		{"messages_meta", func(c *Conn) (any, error) { return wrap(c.MessagesMeta(DefaultMessageKinds, 200)) }},
		{"messages_meta_kinds", func(c *Conn) (any, error) { return wrap(c.MessagesMeta([]string{"msg", "request"}, 2)) }},
		{"lifecycle_t1", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-1", testNow)) }},
		{"lifecycle_t2", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-2", testNow)) }},
		{"lifecycle_t3", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-3", testNow)) }},
		{"lifecycle_t5", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-5", testNow)) }},
		{"lifecycle_t9", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-9", testNow)) }},
		{"lifecycle_t12", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-12", testNow)) }},
		{"lifecycle_none", func(c *Conn) (any, error) { return wrap(c.TaskLifecycle("T-404", testNow)) }},
		{"throughput", func(c *Conn) (any, error) { return wrap(c.Throughput(14, testNow, nil)) }},
		{"throughput_7", func(c *Conn) (any, error) { return wrap(c.Throughput(7, testNow, nil)) }},
		{"throughput_goals", func(c *Conn) (any, error) { return wrap(c.Throughput(14, testNow, GoalSet{"G-002": true})) }},
		{"attended", func(c *Conn) (any, error) {
			m, err := c.AttendedTasks(allTasks)
			ids := []string{}
			for id := range m {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return ids, err
		}},
		{"quality", func(c *Conn) (any, error) { return wrap(c.Quality(nil)) }},
		{"quality_goals", func(c *Conn) (any, error) { return wrap(c.Quality(goalsG1)) }},
		{"changelog", func(c *Conn) (any, error) { return wrap(c.Changelog(nil)) }},
		{"changelog_goals", func(c *Conn) (any, error) { return wrap(c.Changelog(GoalSet{"G-002": true})) }},
		{"config", func(c *Conn) (any, error) { return wrap(c.Config()) }},
		{"resources_db", func(c *Conn) (any, error) { return wrap(c.ResourcesDB(defaults)) }},
		{"resources_db_nodefaults", func(c *Conn) (any, error) { return wrap(c.ResourcesDB(nil)) }},
		{"shell_ok", func(c *Conn) (any, error) {
			m := 4096.0
			return wrap(c.ShellStatus(usageOK, defaults, &m, testNow, stubShell))
		}},
		{"shell_memlow", func(c *Conn) (any, error) {
			m := 100.0
			return wrap(c.ShellStatus(usageOK, defaults, &m, testNow, stubShell))
		}},
		{"shell_nousage", func(c *Conn) (any, error) { return wrap(c.ShellStatus(nil, defaults, nil, testNow, stubShell)) }},
		{"shell_cap", func(c *Conn) (any, error) {
			return wrap(c.ShellStatus(&Usage{FiveHour: fp(95), SevenDay: fp(10), Fresh: true}, defaults, nil, testNow, stubShell))
		}},
		{"shell_high", func(c *Conn) (any, error) {
			return wrap(c.ShellStatus(&Usage{FiveHour: fp(80.4), SevenDay: fp(10), Fresh: true}, defaults, nil, testNow, stubShell))
		}},
		{"shell_weekly", func(c *Conn) (any, error) {
			return wrap(c.ShellStatus(&Usage{FiveHour: fp(10), SevenDay: fp(74.6), Fresh: true, AgeMin: 5}, defaults, nil, testNow, stubShell))
		}},
		{"shell_stale", func(c *Conn) (any, error) {
			return wrap(c.ShellStatus(&Usage{FiveHour: fp(10), SevenDay: fp(10), Fresh: true, AgeMin: 31}, defaults, nil, testNow, stubShell))
		}},
		{"last_event_by_agent", func(c *Conn) (any, error) { return wrap(c.LastEventByAgent()) }},
		{"last_activity_by_goal", func(c *Conn) (any, error) { return wrap(c.LastActivityByGoal()) }},
		{"recent_merges", func(c *Conn) (any, error) { return wrap(c.RecentMerges(5, nil)) }},
		{"recent_merges_2", func(c *Conn) (any, error) { return wrap(c.RecentMerges(2, nil)) }},
		{"recent_merges_goals", func(c *Conn) (any, error) { return wrap(c.RecentMerges(5, GoalSet{"G-002": true})) }},
		{"merges_per_day", func(c *Conn) (any, error) { return wrap(c.MergesPerDay(14, testNow, nil)) }},
		{"merges_per_day_goals", func(c *Conn) (any, error) { return wrap(c.MergesPerDay(5, testNow, goalsG1)) }},
		{"bouncing", func(c *Conn) (any, error) { return wrap(c.BouncingTasks(5, nil)) }},
		{"bouncing_1", func(c *Conn) (any, error) { return wrap(c.BouncingTasks(1, nil)) }},
		{"bouncing_goals", func(c *Conn) (any, error) { return wrap(c.BouncingTasks(5, GoalSet{"G-002": true})) }},
		{"message_refs", func(c *Conn) (any, error) { return wrap(c.MessageRefs([]int64{1, 2, 3, 6, 99})) }},
		{"task_titles", func(c *Conn) (any, error) { return wrap(c.TaskTitles([]string{"T-1", "T-11", "T-99", "T-2"}, 80)) }},
		{"task_titles_w", func(c *Conn) (any, error) { return wrap(c.TaskTitles([]string{"T-11"}, 6)) }},
		{"criteria_by_goal", func(c *Conn) (any, error) { return wrap(c.CriteriaByGoal()) }},
		{"quality_windows", func(c *Conn) (any, error) { return wrap(c.QualityWindows(14, testNow, nil)) }},
		{"quality_windows_7", func(c *Conn) (any, error) { return wrap(c.QualityWindows(7, testNow, goalsG1)) }},
		{"ctx_baseline", func(c *Conn) (any, error) { return wrap(Baseline(projectsRoot(), c, "", "")) }},
		{"ctx_baseline_window", func(c *Conn) (any, error) { return wrap(Baseline(projectsRoot(), c, "2026-11-05", "2026-11-14")) }},
		{"ctx_merged_tasks", func(c *Conn) (any, error) { return wrap(c.MergedTasks("", "", 0)) }},
		{"ctx_merged_window", func(c *Conn) (any, error) { return wrap(c.MergedTasks("2026-11-08", "2026-11-12", 2)) }},
		{"ctx_per_pr", func(c *Conn) (any, error) { return wrap(PerPR(projectsRoot(), c, "", "", 0)) }},
		{"ctx_per_pr_last", func(c *Conn) (any, error) { return wrap(PerPR(projectsRoot(), c, "", "", 2)) }},
		{"ctx_per_pr_since", func(c *Conn) (any, error) { return wrap(PerPR(projectsRoot(), c, "2026-11-08", "", 0)) }},
		{"ctx_per_pr_empty", func(c *Conn) (any, error) { return wrap(PerPR(projectsRoot(), c, "2030-01-01", "", 0)) }},
		{"audit", func(c *Conn) (any, error) { return wrap(Audit(projectsRoot())) }},
	}
}

// TestQueriesGolden compares every query function on the seeded fixture with the json.dumps output of the Python
// function on the same database (testdata/golden/<case>.json), byte for byte: values, NULLs, row order, key order.
func TestQueriesGolden(t *testing.T) {
	c := openFixture(t)
	cases := goldenCases()
	files, _ := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if len(files) != len(cases) {
		t.Errorf("%d golden files but %d cases: every golden must have a case and the other way round", len(files), len(cases))
	}
	for _, gc := range cases {
		gc := gc
		t.Run(gc.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "golden", gc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := gc.run(c)
			if err != nil {
				t.Fatal(err)
			}
			b, err := Dumps(got)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := string(b), strings.TrimRight(string(want), "\n"); g != w {
				t.Errorf("%s differs from the Python golden\n%s", gc.name, firstDiff(g, w))
			}
		})
	}
}

func firstDiff(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	lo := i - 40
	if lo < 0 {
		lo = 0
	}
	cut := func(s string) string {
		hi := i + 60
		if hi > len(s) {
			hi = len(s)
		}
		if lo > hi {
			return ""
		}
		return s[lo:hi]
	}
	return fmt.Sprintf("first difference at byte %d:\n  got : …%s\n  want: …%s", i, cut(a), cut(b))
}

// TestTextReportsGolden checks the text renderers (context.render, render_per_pr, advisor_audit.render).
func TestTextReportsGolden(t *testing.T) {
	c := openFixture(t)
	base, err := Baseline(projectsRoot(), c, "", "")
	if err != nil {
		t.Fatal(err)
	}
	all, err := PerPR(projectsRoot(), c, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	last, err := PerPR(projectsRoot(), c, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Audit(projectsRoot())
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"ctx_baseline": base.Render(), "ctx_per_pr": all.Render(), "ctx_per_pr_last": last.Render(), "audit": RenderAudit(rows)} {
		want, err := os.ReadFile(filepath.Join("testdata", "golden", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s text differs\n got: %q\nwant: %q\n%s", name, got, string(want), firstDiff(got, string(want)))
		}
	}
}
