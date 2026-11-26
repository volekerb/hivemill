package hm

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The task page takes both themes from app.css and loads nothing from outside. The one http address it may hold is the
// task's PR, as a link a reader follows.
func TestObsTracePageThemes(t *testing.T) {
	h, err := traceRunner(t).ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	_, css := obsGet(t, h, "/assets/obs/app.css")
	if !strings.Contains(css, "@media (prefers-color-scheme: dark)") {
		t.Error("the shared stylesheet has no dark rule")
	}
	ref := regexp.MustCompile(`(?i)<([a-z][a-z0-9]*)\b[^>]*?\s(src|href)\s*=\s*["']?\s*((?:https?:|//)[^"'\s>]*)`)
	for _, target := range []string{"/task?k=box-a/T-1", "/task?k=box-a/T-1&theme=light", "/task?k=box-a/T-1&theme=dark", "/task?k=box-a/T-2"} {
		code, body := obsGet(t, h, target)
		if code != http.StatusOK || !strings.Contains(body, `<link rel="stylesheet" href="/assets/obs/app.css">`) {
			t.Fatalf("%s: %d, no shared stylesheet:\n%s", target, code, body)
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "<style") || strings.Contains(body, "style=") {
			t.Errorf("%s: a script or its own style", target)
		}
		for _, m := range ref.FindAllStringSubmatch(body, -1) {
			if m[1] != "a" || m[2] != "href" || m[3] != tracePR {
				t.Errorf("%s: external reference %q", target, m[0])
			}
		}
	}
}

func TestObsTracePage(t *testing.T) {
	h, err := traceRunner(t).ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	code, body := obsGet(t, h, "/task?k=box-a/T-1")
	head, raw, collapsed := strings.Cut(body, "<details><summary>Raw sessions and events</summary>")
	if code != http.StatusOK || !collapsed {
		t.Fatalf("%d, no collapsed raw block:\n%s", code, body)
	}
	for _, want := range []string{"<dt>goal</dt><dd>G-1", `<dt>status</dt><dd class="ok">merged`, "<dt>total cost</dt><dd>$3.00",
		`<a href="` + tracePR + `" rel="noreferrer">PR</a>`, `<a href="/session?s=s-task-1">`, "$2.50",
		`<td class="bad"><strong>blocked</strong>`, `<td class="warn"><strong>held</strong>`,
		"<dt>machine / harness</dt>", "<dt>cost split</dt><dd>task $", "<dt>rounds</dt>", "<dt>findings</dt>", "<dt>trace id</dt><dd><code>"} {
		if !strings.Contains(head, want) {
			t.Errorf("trace lacks %q:\n%s", want, head)
		}
	}
	steps := traceOf(t, "box-a/T-1").Steps
	if n := strings.Count(head, "<tr data-step="); n != len(steps) {
		t.Errorf("%d rows for %d steps", n, len(steps))
	}
	if !strings.Contains(raw, "<h2>Sessions</h2>") || !strings.Contains(raw, "<h2>Timeline</h2>") {
		t.Errorf("the raw lists are not in the collapsed block:\n%s", raw)
	}

	_, body = obsGet(t, h, "/task?k=box-a/T-2&theme=dark")
	for _, want := range []string{`data-theme="dark"`, `<dt>status</dt><dd class="bad">FAIL`, "<dt>total cost</dt><dd>$1.25",
		`<td class="bad"><strong>failed</strong>`, "failed: gate blocked 3 times", `href="/session?s=s-task-2&amp;theme=dark"`} {
		if !strings.Contains(body, want) {
			t.Errorf("failed task lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "style=") {
		t.Error("no script and no inline style")
	}
	if m, class := stepMark(TraceStep{Kind: "judge.verdict", Summary: "judge MERGE: all met"}); m != "" || class != "" {
		t.Errorf("a merge verdict is marked %q", m)
	}
	for summary, want := range map[string]string{"judge REJECT: criterion 2 unmet": "blocked", "judge HOLD: owner": "held"} {
		if m, _ := stepMark(TraceStep{Kind: "judge.verdict", Summary: summary}); m != want {
			t.Errorf("%q is marked %q", summary, m)
		}
	}

	for _, target := range []string{"/session?s=s-task-1", "/task?k=nope"} {
		if _, body = obsGet(t, h, target); strings.Contains(body, "<details><summary>Raw sessions") {
			t.Errorf("%s has the trace page", target)
		}
	}

	// A session with no session.ended line and no transcript: its cost is unknown, so the total says it is partial.
	r := traceRunner(t)
	writeEvents(t, r.Log.Path, ev(time.Now().UTC().Format(time.RFC3339), "session.started", "T-9", "G-1", "www", "s-open", "task", nil))
	if h, err = r.ObsHandler(); err != nil {
		t.Fatal(err)
	}
	if _, body = obsGet(t, h, "/task?k=box-a/T-9"); !strings.Contains(body, "partial: a session cost is pending") ||
		strings.Contains(head, "partial:") {
		t.Errorf("the pending total is not marked:\n%s", body)
	}
}
