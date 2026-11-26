package hm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tracePR = "https://github.com/zz-test/www/pull/7"

// traceEvents: T-1 held, then merged; T-2 failed. Lines are written out of time order on purpose.
func traceEvents(day string) []Event {
	ts := func(h string) string { return day + "T" + h + ":00Z" }
	at := func(h, kind, task, sid, role string, data map[string]any) Event {
		return ev(ts(h), kind, task, "G-1", "www", sid, role, data)
	}
	return []Event{
		cost(at("03:10", "session.ended", "T-1", "s-task-1", "task", nil), 2.50, 30, 40),
		at("01:00", "task.dispatched", "T-1", "", "", nil),
		at("01:01", "session.started", "T-1", "s-task-1", "task", nil),
		at("01:02", "prompt.sent", "T-1", "s-task-1", "task", map[string]any{"kind": "kickoff", "path": "runs/T-1/kickoff.md",
			"text": "SECRET PROMPT"}),
		at("01:10", "gate.deferred", "T-1", "s-task-1", "task", map[string]any{"why": "no PR yet"}),
		at("01:20", "gate.blocked", "T-1", "s-task-1", "task", map[string]any{"problems": []any{"no evidence", "red\nproof"}}),
		at("01:30", "review.findings", "T-1", "s-task-1", "review", map[string]any{"count": 2.0, "fixed": 1.0}),
		at("01:31", "gate.passed", "T-1", "s-task-1", "task", map[string]any{"reruns": []any{
			map[string]any{"id": 1.0, "kind": "cmd", "note": "exit 0"}}}),
		at("02:00", "ci.red", "T-1", "", "", map[string]any{"failed": []any{"go"}, "ci_round": 1.0}),
		at("02:20", "judge.verdict", "T-1", "s-judge-1", "judge", map[string]any{"verdict": "MERGE", "reasons": []any{"all met"}}),
		at("02:30", "run.held", "T-1", "", "", map[string]any{"why": "T3", "pr": tracePR}),
		at("02:40", "session.closed", "T-1", "", "task", nil),
		at("03:00", "pr.merged", "T-1", "", "", map[string]any{"pr": tracePR}),
		cost(at("03:05", "session.ended", "T-1", "s-task-1", "task", nil), 1.00, 10, 20), // cumulative: the later line wins
		cost(at("03:11", "session.ended", "T-1", "s-judge-1", "judge", nil), 0.50, 5, 6),
		at("04:00", "task.dispatched", "T-2", "", "", nil),
		at("04:01", "session.started", "T-2", "s-task-2", "task", nil),
		cost(at("05:00", "session.ended", "T-2", "s-task-2", "task", nil), 1.25, 1, 2),
		at("05:00", "task.failed", "T-2", "", "", map[string]any{"reason": "gate blocked 3 times"}),
	}
}

func traceRunner(t *testing.T) *Runner {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	writeEvents(t, r.Log.Path, traceEvents(time.Now().UTC().Format("2006-01-02"))...)
	return r
}

func traceOf(t *testing.T, key string) Trace {
	t.Helper()
	o, err := traceRunner(t).LoadObs(ObsFilter{}, true, time.Now())
	if err != nil || o.Tasks[key] == nil {
		t.Fatalf("no task %s: %v", key, err)
	}
	return buildTrace(o.Tasks[key])
}

func kinds(tr Trace) []string {
	var out []string
	for _, s := range tr.Steps {
		out = append(out, s.Kind)
	}
	return out
}

func TestObsTraceOrderAndCost(t *testing.T) {
	tr := traceOf(t, "box-a/T-1")
	want := []string{"task.dispatched", "session.started", "prompt.sent", "gate.deferred", "gate.blocked", "review.findings",
		"gate.passed", "ci.red", "judge.verdict", "run.held", "pr.merged", "session.ended", "session.ended"}
	if !reflect.DeepEqual(kinds(tr), want) {
		t.Fatalf("steps: %v", kinds(tr))
	}
	sum := 0.0
	for i, s := range tr.Steps {
		if i > 0 && parseTS(s.TS).Before(parseTS(tr.Steps[i-1].TS)) {
			t.Fatalf("out of order at %d: %+v", i, tr.Steps)
		}
		if s.CostUSD != nil {
			sum += *s.CostUSD
		}
	}
	if tr.Task != "T-1" || tr.Goal != "G-1" || tr.Project != "www" || tr.TotalUSD != 3.0 || sum != 3.0 {
		t.Fatalf("trace %s %s %s total %v steps %v", tr.Task, tr.Goal, tr.Project, tr.TotalUSD, sum)
	}
	for i, line := range map[int]string{
		2: "prompt sent: kickoff runs/T-1/kickoff.md", 3: "gate deferred: no PR yet", 4: "gate blocked: no evidence; red proof",
		5: "review: 2 findings, 1 fixed", 6: "gate passed: 1 cmd exit 0", 7: "CI red (round 1): go",
		8: "judge MERGE: all met", 9: "held: T3", 11: "task session ended on box-a, $2.50"} {
		if tr.Steps[i].Summary != line {
			t.Errorf("step %d: %q, want %q", i, tr.Steps[i].Summary, line)
		}
	}
	if b, _ := json.Marshal(tr); strings.Contains(string(b), "SECRET") {
		t.Fatal("the prompt text reached the trace")
	}
	failed := traceOf(t, "box-a/T-2")
	last := failed.Steps[len(failed.Steps)-1]
	if last.Kind != "task.failed" || last.Summary != "failed: gate blocked 3 times" || failed.TotalUSD != 1.25 {
		t.Fatalf("failed task: %+v total %v", last, failed.TotalUSD)
	}
}

func TestObsTraceLinks(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range traceOf(t, "box-a/T-1").Steps {
		seen[s.Kind] = true
		switch s.Kind {
		case "pr.merged":
			if s.Links.PR != tracePR || s.Links.Session != "" {
				t.Fatalf("merged links: %+v", s.Links)
			}
		case "session.started":
			if s.Links.Session != "/session?s=s-task-1" || s.Role != "task" || s.Machine != "box-a" {
				t.Fatalf("session step: %+v", s)
			}
		case "session.ended":
			if !strings.HasPrefix(s.Links.Session, "/session?s=s-") {
				t.Fatalf("ended links: %+v", s.Links)
			}
		}
	}
	if !seen["pr.merged"] || !seen["session.started"] || !seen["session.ended"] {
		t.Fatalf("missing steps: %v", seen)
	}
}

func TestObsTraceAPI(t *testing.T) {
	h, err := traceRunner(t).ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req.Host = "127.0.0.1:7461"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := do("GET", "/api/obs/task?k=box-a/T-1")
	var tr Trace
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil || w.Code != http.StatusOK ||
		w.Header().Get("Content-Type") != "application/json" || tr.Task != "T-1" || len(tr.Steps) == 0 || tr.TotalUSD != 3.0 {
		t.Fatalf("GET: %d %v %s", w.Code, err, w.Body)
	}
	for _, target := range []string{"/api/obs/task?k=box-a/T-9", "/api/obs/task?k=box-a/T-1&machine=box-z"} {
		w = do("GET", target)
		var e map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || w.Code != http.StatusNotFound || e["error"] == "" {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	for _, method := range []string{"POST", "HEAD", "PUT", "DELETE"} {
		if w = do(method, "/api/obs/task?k=box-a/T-1"); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET" {
			t.Fatalf("%s: %d %q", method, w.Code, w.Header().Get("Allow"))
		}
	}
}
