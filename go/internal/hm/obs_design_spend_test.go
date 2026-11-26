package hm

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// spendDesignPage is the Spend page over testdata/obs_fixture and the page model /api/obs/spend gives for it.
func spendDesignPage(t *testing.T, h http.Handler, query string) (string, SpendPage) {
	t.Helper()
	code, body := obsGet(t, h, "/api/obs/spend"+query)
	var api struct {
		Spend SpendPage `json:"spend"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil {
		t.Fatalf("api %d: %s", code, body)
	}
	code, page := obsGet(t, h, "/spend"+query)
	if code != 200 {
		t.Fatalf("page %d: %s", code, page)
	}
	return page, api.Spend
}

func TestObsSpendDesignTotalAboveChart(t *testing.T) {
	// The report's rule for the fixture: every session ends once, and its session.ended line carries its cost.
	var report float64
	for _, name := range []string{"events.jsonl", filepath.Join("box-b", "events.jsonl")} {
		b, err := os.ReadFile(filepath.Join("testdata", "obs_fixture", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var e Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatal(err)
			}
			if e.Event == "session.ended" && e.CostUSD != nil {
				report += *e.CostUSD
			}
		}
	}
	if report <= 0 {
		t.Fatal("the fixture priced nothing")
	}
	h := obsFixtureHandler(t)
	page, api := spendDesignPage(t, h, "")
	if math.Abs(api.Total-report) > 0.005 {
		t.Fatalf("total %.4f, report %.4f", api.Total, report)
	}
	total := fmt.Sprintf(`<h1 class="spend-total">$%.2f in %d days`, report, api.Days)
	at, svg := strings.Index(page, total), strings.Index(page, "<svg")
	if at < 0 || svg < 0 || at > svg {
		t.Fatalf("want %q before the svg (at %d, svg %d):\n%s", total, at, svg, page)
	}
	if api.Pending > 0 && !strings.Contains(page[at:svg], fmt.Sprintf("(%d sessions with no cost yet)", api.Pending)) {
		t.Errorf("the total lost its pending note:\n%s", page[at:svg])
	}
}

func TestObsSpendDesignChartAccessible(t *testing.T) {
	page, api := spendDesignPage(t, obsFixtureHandler(t), "")
	svg := regexp.MustCompile(`(?s)<svg [^>]*role="img" aria-label="USD per day">.*?</svg>`).FindString(page)
	if svg == "" {
		t.Fatalf("no svg with role img and a name:\n%s", page)
	}
	for i, name := range []string{"task", "judge", "planner"} {
		legend := regexp.MustCompile(fmt.Sprintf(`<rect class="s%d" [^>]*width="10" height="10"/><text class="lg" [^>]*>%s</text>`, i, name))
		if !legend.MatchString(svg) {
			t.Errorf("no legend entry for %s", name)
		}
	}
	day, top := "", 0.0
	for _, d := range api.Daily {
		if sum := d.Task + d.Judge + d.Planner; sum > top {
			day, top = d.Day, sum
		}
	}
	if want := fmt.Sprintf(`<p class="summary muted">Highest day: %s at $%.2f.</p>`, day, top); !strings.Contains(page, want) {
		t.Errorf("no summary line %q:\n%s", want, page)
	}
	details := regexp.MustCompile(`(?s)<details><summary>[^<]+</summary>.*?</details>`).FindString(page)
	for _, h := range []string{"day (UTC)", "task", "judge", "planner"} {
		if !regexp.MustCompile(`<th scope="col"[^>]*>` + regexp.QuoteMeta(h) + `</th>`).MatchString(details) {
			t.Errorf("no column header %s:\n%s", h, details)
		}
	}
	rows := regexp.MustCompile(`<tr><th scope="row">([^<]*)</th><td class="num">([^<]*)</td><td class="num">([^<]*)</td><td class="num">([^<]*)</td></tr>`).FindAllStringSubmatch(details, -1)
	if len(api.Daily) == 0 || len(rows) != len(api.Daily) {
		t.Fatalf("%d value rows for %d days:\n%s", len(rows), len(api.Daily), details)
	}
	usd := func(f float64) string { return fmt.Sprintf("$%.2f", f) }
	for i, d := range api.Daily {
		got, want := strings.Join(rows[i][1:], " "), strings.Join([]string{d.Day, usd(d.Task), usd(d.Judge), usd(d.Planner)}, " ")
		if got != want {
			t.Errorf("row %d: %s, want %s", i, got, want)
		}
		hit := fmt.Sprintf("<title>%s: task %s, judge %s, planner %s</title>", d.Day, usd(d.Task), usd(d.Judge), usd(d.Planner))
		if !strings.Contains(svg, hit) {
			t.Errorf("the chart does not draw %q", hit)
		}
	}
}

func TestObsSpendDesignGroupByActive(t *testing.T) {
	h := obsFixtureHandler(t)
	link := regexp.MustCompile(`<a href="/spend\?[^"]*\bby=([a-z]+)[^"]*"( aria-current="true")?>`)
	for _, by := range SpendGroups {
		page, _ := spendDesignPage(t, h, "?by="+by)
		row := regexp.MustCompile(`<p class="group">.*?</p>`).FindString(page)
		var all, marked []string
		for _, m := range link.FindAllStringSubmatch(row, -1) {
			all = append(all, m[1])
			if m[2] != "" {
				marked = append(marked, m[1])
			}
		}
		if strings.Join(all, " ") != strings.Join(SpendGroups, " ") || len(marked) != 1 || marked[0] != by ||
			strings.Count(row, "aria-current") != 1 {
			t.Errorf("by %s: links %v, marked %v:\n%s", by, all, marked, row)
		}
		if at := strings.Index(page, `<p class="group">`); at < strings.Index(page, "<svg") || at > strings.Index(page, `<table class="groups">`) {
			t.Errorf("by %s: the group row is not between the chart and the table", by)
		}
	}
}

func TestObsSpendDesignRowsSetApart(t *testing.T) {
	h := obsFixtureHandler(t)
	for _, by := range SpendGroups {
		page, api := spendDesignPage(t, h, "?by="+by)
		table := regexp.MustCompile(`(?s)<table class="groups">.*?</table>`).FindString(page)
		rows := regexp.MustCompile(`(?s)<tr( class="[^"]*")?>(.*?)</tr>`).FindAllStringSubmatch(table, -1)
		if len(api.Rows) == 0 || len(rows) != len(api.Rows)+3 { // header, data, unattributed, total
			t.Fatalf("by %s: %d table rows for %d data rows:\n%s", by, len(rows), len(api.Rows), table)
		}
		for i, r := range rows[:len(rows)-2] {
			if r[1] != "" {
				t.Errorf("by %s: row %d has %s", by, i, r[1])
			}
		}
		un, total := rows[len(rows)-2], rows[len(rows)-1]
		if un[1] != ` class="unattributed total"` || !strings.Contains(un[2], "of which unattributed") {
			t.Errorf("by %s: unattributed row %q", by, un[0])
		}
		if total[1] != ` class="total"` || !strings.HasPrefix(total[2], "<td>total</td>") {
			t.Errorf("by %s: total row %q", by, total[0])
		}
	}
}
