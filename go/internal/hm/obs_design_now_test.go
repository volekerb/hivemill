package hm

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// the Now page's sections in the order docs/design/obs-ui.md gives them.
var obsNowSections = []struct{ id, title string }{
	{"held", "Held"}, {"questions", "Open questions"}, {"running", "Running"},
	{"planner", "Planner"}, {"projects", "Projects"}, {"machines", "Machines"},
}

func obsNowBody(t *testing.T, h http.Handler) string {
	t.Helper()
	code, body := obsGet(t, h, "/")
	if code != http.StatusOK || !strings.Contains(body, "</html>") {
		t.Fatalf("GET /: code %d\n%s", code, body)
	}
	return body
}

// obsNowSection is the markup between a section's heading and the next one.
func obsNowSection(t *testing.T, body, id string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `<h2 id="`+id+`">`)
	if !ok {
		t.Fatalf("no section %q", id)
	}
	rest, _, _ = strings.Cut(rest, "<h2")
	rest, _, _ = strings.Cut(rest, "<footer")
	return rest
}

func TestObsNowSectionOrder(t *testing.T) {
	body := obsNowBody(t, obsFixtureHandler(t))
	var got, want []string
	for _, m := range regexp.MustCompile(`<h2[^>]*>([^<]*)</h2>`).FindAllStringSubmatch(body, -1) {
		got = append(got, m[1])
	}
	for _, s := range obsNowSections {
		want = append(want, s.title)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sections %q, want %q", got, want)
	}
}

func TestObsNowCountsLineLinks(t *testing.T) {
	body := obsNowBody(t, obsFixtureHandler(t))
	_, after, _ := strings.Cut(body, "</h1>")
	line, _, ok := strings.Cut(strings.TrimSpace(after), "</p>")
	if !ok || !strings.HasPrefix(line, "<p>") {
		t.Fatalf("no counts line under the title: %.200s", after)
	}
	for _, s := range obsNowSections {
		if !strings.Contains(line, `href="#`+s.id+`"`) {
			t.Errorf("counts line has no link to #%s: %s", s.id, line)
		}
		if n := strings.Count(body, `id="`+s.id+`"`); n != 1 {
			t.Errorf("anchor %q appears %d times", s.id, n)
		}
	}
	for _, re := range []string{`>\d+ held<`, `>\d+ open questions?<`, `>\d+ running<`} {
		if !regexp.MustCompile(re).MatchString(line) {
			t.Errorf("counts line lacks %s: %s", re, line)
		}
	}
}

func TestObsNowEmptyAttentionOneLine(t *testing.T) {
	r, _ := testRunner(t, "")
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	body := obsNowBody(t, h)
	for _, c := range []struct{ id, say string }{{"held", "no held run"}, {"questions", "no open question"}} {
		sec := obsNowSection(t, body, c.id)
		if strings.Contains(sec, "<table") || strings.Count(sec, "<p") != 1 {
			t.Errorf("%s: want one line and no table: %s", c.id, sec)
		}
		if !strings.Contains(sec, c.say) || !strings.Contains(sec, "Next:") || !strings.Contains(sec, `href="#running"`) {
			t.Errorf("%s: want %q and a next step: %s", c.id, c.say, sec)
		}
	}
}

func TestObsNowProjectsEmptyAndPending(t *testing.T) {
	r, _ := testRunner(t, "")
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	sec := obsNowSection(t, obsNowBody(t, h), "projects")
	if !strings.Contains(sec, "no project yet") || strings.Contains(sec, "<table") || strings.Contains(sec, "in this range") {
		t.Fatalf("empty projects: %s", sec)
	}

	// a task session that has started and reported no cost yet
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	writeEvents(t, r.Log.Path,
		ev(ts, "task.dispatched", "T-1", "G-1", "www", "", "", nil),
		ev(ts, "session.started", "T-1", "G-1", "www", "s-live", "task", nil))
	sec = obsNowSection(t, obsNowBody(t, h), "projects")
	if !strings.Contains(sec, "(1 pending)") || !strings.Contains(sec, "Pending: sessions in the range with no cost yet") {
		t.Errorf("no visible pending note: %s", sec)
	}
	_, details, ok := strings.Cut(sec, "<details>")
	details, _, closed := strings.Cut(details, "</details>")
	if !ok || !closed || !strings.Contains(details, "at API prices") || strings.Count(sec, "at API prices") != 1 {
		t.Errorf("footnote is not inside a details element: %s", sec)
	}
}
