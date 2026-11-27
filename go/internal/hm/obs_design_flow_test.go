package hm

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// obsFlowPage renders /flow over testdata/obs_fixture.
func obsFlowPage(t *testing.T) (http.Handler, string) {
	t.Helper()
	h := obsFixtureHandler(t)
	code, body := obsGet(t, h, "/flow")
	if code != 200 {
		t.Fatalf("/flow: %d\n%s", code, body)
	}
	return h, body
}

// obsFlowSection is the page from the heading that starts with title up to the next heading or the footer.
func obsFlowSection(t *testing.T, body, title string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, "<h2>"+title)
	if !ok {
		t.Fatalf("no heading %q:\n%s", title, body)
	}
	for _, end := range []string{"<h2", "<footer"} {
		rest, _, _ = strings.Cut(rest, end)
	}
	return rest
}

func TestObsFlowDesignSectionOrder(t *testing.T) {
	_, body := obsFlowPage(t)
	var got []string
	for _, m := range regexp.MustCompile(`<h2[^>]*>([^<(]*)`).FindAllStringSubmatch(body, -1) {
		got = append(got, strings.TrimSpace(m[1]))
	}
	if want := "Stuck now|Time per stage|Merged per day"; strings.Join(got, "|") != want {
		t.Errorf("headings %q, want %q", got, want)
	}
}

func TestObsFlowDesignStuckLeadsWithTask(t *testing.T) {
	h, _ := obsFlowPage(t)
	// Every run of the fixture has ended or is held, so two runs are left open here for three and five hours.
	ago := func(hours int) string {
		return time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
	}
	writeEvents(t, h.(*obsServer).r.Log.Path,
		ev(ago(3), "task.dispatched", "T-901", "G-001", "demo-app", "", "", nil),
		ev(ago(5), "task.dispatched", "T-902", "G-001", "demo-app", "", "", nil))
	_, body := obsGet(t, h, "/flow")
	sec := obsFlowSection(t, body, "Stuck now")
	rows := regexp.MustCompile(`<tr><td[^>]*>.*?</tr>`).FindAllString(sec, -1)
	if len(rows) != 2 {
		t.Fatalf("two stuck rows:\n%s", sec)
	}
	lead := regexp.MustCompile(`^<tr><td><a href="/task\?k=[^"]+">[^<]+</a></td><td class="num warn">(\d+h \d\dm|\d+m|\d+s)</td>`)
	for _, row := range rows {
		if !lead.MatchString(row) {
			t.Errorf("a stuck row leads with the task link and the time in stage: %s", row)
		}
	}
	// An empty stuck table ignores the range, so it never blames the range and it says where to look next.
	_, empty := obsGet(t, h, "/flow?project=none-such")
	sec = obsFlowSection(t, empty, "Stuck now")
	if !strings.Contains(sec, "nothing is stuck") || !strings.Contains(sec, "<a href=") || strings.Contains(sec, "range") || strings.Contains(sec, "<table") {
		t.Errorf("empty Stuck now:\n%s", sec)
	}
	for title, want := range map[string]string{"Time per stage": "no task in this range", "Merged per day": "no merged PR in this range"} {
		sec = obsFlowSection(t, empty, title)
		if !strings.Contains(sec, want) || !strings.Contains(sec, "days=90") || strings.Contains(sec, "<table") || strings.Contains(sec, "<svg") {
			t.Errorf("empty %s: want %q and a link to a wider range:\n%s", title, want, sec)
		}
	}
}

func TestObsFlowDesignStageMeanings(t *testing.T) {
	_, body := obsFlowPage(t)
	sec := obsFlowSection(t, body, "Time per stage")
	meanings := regexp.MustCompile(`^[^<]*</h2>\s*<p class="muted">dispatched: [^<]*gate: [^<]*ci: [^<]*judge: [^<]*merge: [^<]*</p>`)
	if !meanings.MatchString(sec) {
		t.Errorf("the stage meanings sit directly under the heading:\n%s", sec)
	}
	// The rule is the left edge of the colgroup that holds the last two columns: held and total.
	groups := regexp.MustCompile(`<table rules="groups"[^>]*>\s*<colgroup>((?:<col>)*)</colgroup><colgroup class="rule"><col><col></colgroup>`).FindStringSubmatch(sec)
	if groups == nil {
		t.Fatalf("no rule class on a two-column group:\n%s", sec)
	}
	var heads []string
	for _, m := range regexp.MustCompile(`<th scope="col"[^>]*>([^<]*)</th>`).FindAllStringSubmatch(sec, -1) {
		heads = append(heads, m[1])
	}
	before := strings.Count(groups[1], "<col>")
	if len(heads) != before+2 || strings.Join(heads[before:], "|") != "held|total" {
		t.Errorf("columns %q with %d before the rule", heads, before)
	}
}

func TestObsFlowDesignChartAccessible(t *testing.T) {
	_, body := obsFlowPage(t)
	sec := obsFlowSection(t, body, "Merged per day")
	if !regexp.MustCompile(`<svg [^>]*role="img" aria-label="[^"]+"`).MatchString(sec) {
		t.Errorf("the svg has no role and name:\n%s", sec)
	}
	if !regexp.MustCompile(`<rect class="s0"[^>]*/><text class="lg"[^>]*>merged</text>`).MatchString(sec) {
		t.Error("no legend")
	}
	summary := regexp.MustCompile(`<p>(\d+) merged in the last 30 days; the most in one day: (\d+) on (\d{4}-\d\d-\d\d)\.</p>`).FindStringSubmatch(sec)
	if summary == nil {
		t.Fatalf("no summary line:\n%s", sec)
	}
	_, table, ok := strings.Cut(sec, "<details><summary>")
	if !ok || !strings.Contains(table, `<th scope="col">day</th><th scope="col" class="num">merged</th>`) {
		t.Fatalf("no details values table with column headers:\n%s", sec)
	}
	var cells []string
	total, top := 0, 0
	for _, m := range regexp.MustCompile(`<tr><th scope="row">([^<]+)</th><td class="num">(\d+)</td></tr>`).FindAllStringSubmatch(table, -1) {
		cells = append(cells, m[1]+": merged "+m[2])
		n, _ := strconv.Atoi(m[2])
		total += n
		top = max(top, n)
	}
	var bars []string
	for _, m := range regexp.MustCompile(`<title>([^<]+)</title></rect>`).FindAllStringSubmatch(sec, -1) {
		bars = append(bars, m[1])
	}
	if len(cells) != 30 || strings.Join(cells, "|") != strings.Join(bars, "|") {
		t.Errorf("table cells %q do not match the chart %q", cells, bars)
	}
	if total == 0 || summary[1] != strconv.Itoa(total) || summary[2] != strconv.Itoa(top) || !strings.Contains(strings.Join(cells, "|"), summary[3]+": merged "+summary[2]) {
		t.Errorf("summary %q, table total %d and top %d", summary[0], total, top)
	}
}
