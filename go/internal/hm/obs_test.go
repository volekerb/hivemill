package hm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fixture machines are box-a (this machine) and box-b (pulled); no real host appears in a test.

func ev(ts, kind, task, goal, project, sid, role string, data map[string]any) Event {
	return Event{TS: ts, Event: kind, TaskID: task, GoalID: goal, Project: project, Session: sid, Role: role, Harness: Harness, Data: data}
}

func cost(e Event, usd float64, in, out int64) Event {
	e.CostUSD = &usd
	e.Data = map[string]any{"input_tokens": float64(in), "output_tokens": float64(out), "cache_read_tokens": float64(1000),
		"cache_write_tokens": float64(10)}
	return e
}

func writeEvents(t *testing.T, path string, evs ...Event) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range evs {
		b, _ := json.Marshal(e)
		_, _ = f.Write(append(b, '\n'))
	}
}

// boxA: T-1 merged after a judge REJECT and one red CI (2 rounds), T-2 failed; a planner session with no project.
func boxA(day string) []Event {
	ts := func(h string) string { return day + "T" + h + ":00Z" }
	return []Event{
		ev(ts("01:00"), "task.dispatched", "T-1", "G-1", "www", "", "", nil),
		ev(ts("01:01"), "session.started", "T-1", "G-1", "www", "s-task-1", "task", nil),
		ev(ts("01:30"), "gate.passed", "T-1", "G-1", "www", "s-task-1", "task", map[string]any{"head": "abc"}),
		ev(ts("01:31"), "review.findings", "T-1", "G-1", "www", "s-task-1", "review", map[string]any{"count": 2.0, "fixed": 2.0}),
		ev(ts("01:40"), "session.started", "T-1", "G-1", "www", "s-judge-1", "judge", nil),
		ev(ts("01:50"), "judge.verdict", "T-1", "G-1", "www", "s-judge-1", "judge", map[string]any{"verdict": "REJECT"}),
		ev(ts("02:00"), "ci.red", "T-1", "G-1", "www", "", "", map[string]any{"failed": []any{"go"}}),
		ev(ts("02:20"), "judge.verdict", "T-1", "G-1", "www", "s-judge-1", "judge", map[string]any{"verdict": "MERGE"}),
		ev(ts("03:00"), "pr.merged", "T-1", "G-1", "www", "", "", map[string]any{"pr": "https://github.com/zz-test/www/pull/7"}),
		cost(ev(ts("03:00"), "session.ended", "T-1", "G-1", "www", "s-task-1", "task", nil), 1.00, 10, 20),
		cost(ev(ts("03:01"), "session.ended", "T-1", "G-1", "www", "s-task-1", "task", nil), 2.50, 30, 40), // cumulative: last wins
		cost(ev(ts("03:01"), "session.ended", "T-1", "G-1", "www", "s-judge-1", "judge", nil), 0.50, 5, 6),
		ev(ts("04:00"), "task.dispatched", "T-2", "G-1", "www", "", "", nil),
		ev(ts("04:01"), "session.started", "T-2", "G-1", "www", "s-task-2", "task", nil),
		ev(ts("05:00"), "task.failed", "T-2", "G-1", "www", "", "", map[string]any{"reason": "gate"}),
		cost(ev(ts("05:00"), "session.ended", "T-2", "G-1", "www", "s-task-2", "task", nil), 1.25, 1, 2),
		ev(ts("06:00"), "planner.restarted", "", "", "", "s-plan", "planner", nil),
	}
}

// boxB: the same task id T-1 on another machine (its own DB), merged in one round, in project api.
func boxB(day string) []Event {
	ts := func(h string) string { return day + "T" + h + ":00Z" }
	out := []Event{
		ev(ts("07:00"), "task.dispatched", "T-1", "G-9", "api", "", "", nil),
		ev(ts("07:01"), "session.started", "T-1", "G-9", "api", "s-b-task", "task", nil),
		ev(ts("08:00"), "judge.verdict", "T-1", "G-9", "api", "s-b-judge", "judge", map[string]any{"verdict": "MERGE"}),
		ev(ts("09:00"), "pr.merged", "T-1", "G-9", "api", "", "", nil),
		cost(ev(ts("09:00"), "session.ended", "T-1", "G-9", "api", "s-b-task", "task", nil), 3.00, 7, 8),
		ev(ts("09:30"), "task.dispatched", "T-3", "G-9", "api", "", "", nil),
		ev(ts("09:31"), "session.started", "T-3", "G-9", "api", "s-b-live", "task", nil), // still running: no cost yet
	}
	for i := range out {
		out[i].Harness = "codex"
	}
	return out
}

func obsRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	day := time.Now().UTC().Format("2006-01-02")
	writeEvents(t, r.Log.Path, boxA(day)...)
	return r, day
}

func TestEmitStampsMachineAndKeepsAHarness(t *testing.T) {
	dir := t.TempDir()
	l := Log{Path: filepath.Join(dir, "events.jsonl"), Machine: "box-a"}
	_ = l.Emit("x.one", nil, Event{})
	_ = l.Emit("x.two", nil, Event{Harness: "codex", Machine: "box-z"})
	evs, err := l.Read()
	if err != nil || len(evs) != 2 {
		t.Fatalf("%v %v", evs, err)
	}
	if evs[0].Machine != "box-a" || evs[0].Harness != "claude-code" || evs[1].Harness != "codex" || evs[1].Machine != "box-z" {
		t.Fatalf("stamps: %+v", evs)
	}
	if MachineName("") == "" || MachineName(" box-c ") != "box-c" {
		t.Fatal("MachineName")
	}
}

func TestPortfolioUsesTheReportRules(t *testing.T) {
	day := "2026-11-20"
	now, _ := time.Parse(time.RFC3339, day+"T12:00:00Z")
	evs := boxA(day)
	for i := range evs {
		evs[i].Machine = "box-a"
	}
	local := map[string]SessUsage{"s-plan": {USD: 4, Input: 1}}
	o := BuildObs(evs, ObsFilter{}, "box-a", local, now)
	t1 := o.Tasks["box-a/T-1"]
	if t1 == nil || t1.Row.Outcome != "merged" || t1.Rounds != 3 || t1.Row.USD != 3.0 || t1.Row.USDJudge != 0.5 {
		t.Fatalf("T-1: %+v", t1)
	}
	if s := o.Sessions["s-task-1"]; s.Usage.Input != 30 || s.Usage.USD != 2.5 || s.End == "" {
		t.Fatalf("last session.ended wins: %+v", s)
	}
	if o.Sessions["s-plan"].Usage.USD != 4 {
		t.Fatal("a local session is priced from its transcript")
	}
	lines := o.Portfolio(30, map[string]int{"www": 2})
	var www, all, none ProjectLine
	for _, l := range lines {
		switch l.Project {
		case "www":
			www = l
		case "all":
			all = l
		case NoProject:
			none = l
		}
	}
	rows := []Row{o.Tasks["box-a/T-1"].Row, o.Tasks["box-a/T-2"].Row}
	want := Summarise(rows)
	if www.Merged != want.Merged || *www.PerMergedPR != *want.PerMergedPR || *www.TwoPlusPct != *want.TwoPlusRoundsPct ||
		*www.MedianHours != *want.MedianHours {
		t.Fatalf("portfolio %+v != Summarise %+v", www, want)
	}
	if www.USDToday != 4.25 || www.USD7d != 4.25 || none.USDToday != 4 || all.USD30d != 8.25 || www.OpenAsks != 2 {
		t.Fatalf("spend: www %+v none %+v all %+v", www, none, all)
	}
	if *www.PerMergedPR != 4.25 || *www.TwoPlusPct != 100 || *www.MedianHours != 2 {
		t.Fatalf("outcomes: %+v", www)
	}
	if g := o.Goals("www"); len(g) != 1 || g[0].Tasks != 2 || g[0].Merged != 1 || g[0].Failed != 1 || g[0].USD != 4.25 {
		t.Fatalf("goals: %+v", g)
	}
}

func TestPullMergesMachinesReadOnlyAndIdempotent(t *testing.T) {
	r, day := obsRunner(t)
	var calls []string
	var raw strings.Builder
	for _, e := range boxB(day) {
		b, _ := json.Marshal(e)
		raw.Write(append(b, '\n'))
	}
	fetch := func(host, path string) ([]byte, error) {
		calls = append(calls, host+" "+path)
		return []byte(raw.String()), nil
	}
	for i := 0; i < 2; i++ {
		label, n, err := r.ObsPull("op@box-b:", "", fetch, time.Now())
		if err != nil || label != "box-b" || n != 7 {
			t.Fatalf("pull: %s %d %v", label, n, err)
		}
	}
	if calls[0] != "op@box-b "+DefaultRemoteLog {
		t.Fatalf("fetch: %v", calls)
	}
	// the local log pulled under another name as well: its lines are not counted twice
	if _, _, err := r.ObsPull(r.Log.Path, "box-a-copy", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	evs, err := LoadObsEvents(r.ObsSources(true))
	if err != nil || len(evs) != len(boxA(day))+7 {
		t.Fatalf("events %d (want %d): %v", len(evs), len(boxA(day))+7, err)
	}
	o := BuildObs(evs, ObsFilter{}, "box-a", nil, time.Now())
	if o.Tasks["box-a/T-1"] == nil || o.Tasks["box-b/T-1"] == nil || o.Tasks["box-b/T-1"].Project != "api" {
		t.Fatalf("both machines' T-1 are separate tasks: %v", o.TaskList("", ""))
	}
	if strings.Join(o.Machines, ",") != "box-a,box-b" || strings.Join(o.Harnesses, ",") != "claude-code,codex" {
		t.Fatalf("machines %v harnesses %v", o.Machines, o.Harnesses)
	}
	b := BuildObs(evs, ObsFilter{Harness: "codex"}, "box-a", nil, time.Now())
	if len(b.Tasks) != 2 || b.Tasks["box-b/T-3"] == nil || b.Sessions["s-b-live"].CostKnown {
		t.Fatalf("harness filter: %v", b.TaskList("", ""))
	}
	for _, bad := range []struct{ spec, as string }{{"-oProxyCommand=x:y", ""}, {"box-b:", "../x"}, {"box-b:", "box-a"},
		{"/tmp/x.jsonl", ""}, {"a b:c", ""}} {
		if _, _, err := r.ObsPull(bad.spec, bad.as, fetch, time.Now()); err == nil {
			t.Errorf("pull %q --as %q: want a refusal", bad.spec, bad.as)
		}
	}
	junk := func(string, string) ([]byte, error) { return []byte("not json\n"), nil }
	if _, _, err := r.ObsPull("box-c:x", "", junk, time.Now()); err == nil {
		t.Error("a file that is no event log is refused")
	}
}

type collector struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	auth   []string
	fail   bool
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	if r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(b, &m) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	c.paths, c.auth = append(c.paths, r.URL.Path), append(c.auth, r.Header.Get("Authorization"))
	if c.fail {
		http.Error(w, "down", 503)
		return
	}
	c.bodies = append(c.bodies, m)
	_, _ = w.Write([]byte("{}"))
}

func spansOf(body map[string]any) []map[string]any {
	var out []map[string]any
	for _, rs := range body["resourceSpans"].([]any) {
		for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
			for _, sp := range ss.(map[string]any)["spans"].([]any) {
				out = append(out, sp.(map[string]any))
			}
		}
	}
	return out
}

func attr(sp map[string]any, k string) any {
	for _, a := range sp["attributes"].([]any) {
		m := a.(map[string]any)
		if m["key"] == k {
			for _, v := range m["value"].(map[string]any) {
				return v
			}
		}
	}
	return nil
}

func TestOtelExportSendsATracePerTask(t *testing.T) {
	r, _ := obsRunner(t)
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	n, err := r.OtelExport(OtelOpts{Endpoint: srv.URL, Headers: OTLPHeaders("Authorization=Bearer%20tok, x-empty")})
	if err != nil || n != 2 || len(c.bodies) != 1 {
		t.Fatalf("export: %d %v %d", n, err, len(c.bodies))
	}
	if c.paths[0] != "/v1/traces" || c.auth[0] != "Bearer tok" {
		t.Fatalf("request: %v %v", c.paths, c.auth)
	}
	spans := spansOf(c.bodies[0])
	hex32, hex16 := regexp.MustCompile(`^[0-9a-f]{32}$`), regexp.MustCompile(`^[0-9a-f]{16}$`)
	byName := map[string][]map[string]any{}
	traces := map[string]bool{}
	for _, sp := range spans {
		if !hex32.MatchString(sp["traceId"].(string)) || !hex16.MatchString(sp["spanId"].(string)) {
			t.Fatalf("ids: %v", sp)
		}
		traces[sp["traceId"].(string)] = true
		byName[sp["name"].(string)] = append(byName[sp["name"].(string)], sp)
	}
	if len(traces) != 2 || len(spans) != 5 { // T-1: root + task + judge; T-2: root + task
		t.Fatalf("traces %d spans %d", len(traces), len(spans))
	}
	root := byName["task T-1"][0]
	if attr(root, "hivemill.project") != "www" || attr(root, "hivemill.goal_id") != "G-1" || attr(root, "hivemill.outcome") != "merged" ||
		attr(root, "hivemill.rounds") != "3" || root["status"].(map[string]any)["code"] != 1.0 {
		t.Fatalf("root: %v", root)
	}
	var judge, task map[string]any
	for _, sp := range byName["session judge"] {
		if attr(sp, "hivemill.task_id") == "T-1" {
			judge = sp
		}
	}
	for _, sp := range byName["session task"] {
		if attr(sp, "hivemill.task_id") == "T-1" {
			task = sp
		}
	}
	if judge["parentSpanId"] != root["spanId"] || attr(task, "hivemill.usd") != 2.5 || attr(task, "gen_ai.usage.input_tokens") != "30" ||
		attr(task, "hivemill.harness") != "claude-code" || attr(task, "hivemill.role") != "task" || attr(task, "hivemill.tokens") != "1080" {
		t.Fatalf("session spans: judge %v task %v", judge, task)
	}
	names := func(sp map[string]any) string {
		var out []string
		for _, e := range sp["events"].([]any) {
			out = append(out, e.(map[string]any)["name"].(string))
		}
		return strings.Join(out, ",")
	}
	if names(judge) != "judge.verdict,judge.verdict" || names(task) != "gate.passed,review.findings" ||
		names(root) != "task.dispatched,ci.red,pr.merged" {
		t.Fatalf("events: judge %q task %q root %q", names(judge), names(task), names(root))
	}
	// the same task exported again keeps its ids
	c2 := &collector{}
	srv2 := httptest.NewServer(c2)
	defer srv2.Close()
	_, _ = r.OtelExport(OtelOpts{Endpoint: srv2.URL + "/v1/traces/"})
	if spansOf(c2.bodies[0])[0]["traceId"] != spans[0]["traceId"] || c2.paths[0] != "/v1/traces" {
		t.Fatal("ids are deterministic and the path is not doubled")
	}
	// --since leaves out tasks with no event in the window
	if n, _ := r.OtelExport(OtelOpts{Endpoint: srv2.URL, Since: time.Now().Add(72 * time.Hour)}); n != 0 {
		t.Fatalf("since: %d", n)
	}
}

func TestOtelTailExportsFinishedTasksOnce(t *testing.T) {
	r, day := obsRunner(t)
	c := &collector{fail: true}
	srv := httptest.NewServer(c)
	defer srv.Close()
	r.OtelTail(time.Now()) // off by default: nothing sent
	if len(c.paths) != 0 {
		t.Fatal("otel_endpoint empty sends nothing")
	}
	if err := r.Store.SetCfg("otel_endpoint", srv.URL); err != nil {
		t.Fatal(err)
	}
	r.OtelTail(time.Now()) // the collector is down: printed, retried next pass
	c.fail = false
	r.OtelTail(time.Now())
	r.OtelTail(time.Now())
	if len(c.paths) != 2 || len(c.bodies) != 1 || len(spansOf(c.bodies[0])) != 5 {
		t.Fatalf("tail: %d posts, %d bodies", len(c.paths), len(c.bodies))
	}
	writeEvents(t, r.Log.Path, Event{TS: day + "T23:00:00Z", Event: "task.cancelled", TaskID: "T-9", Machine: "box-a"})
	r.OtelTail(time.Now())
	if len(c.bodies) != 2 || len(spansOf(c.bodies[1])) != 1 {
		t.Fatalf("only the newly finished task goes out: %d", len(c.bodies))
	}
}

func TestObsPageIsReadOnlyLoopbackAndZooms(t *testing.T) {
	r, day := obsRunner(t)
	var raw strings.Builder
	for _, e := range boxB(day) {
		b, _ := json.Marshal(e)
		raw.Write(append(b, '\n'))
	}
	if _, _, err := r.ObsPull("box-b:x", "", func(string, string) ([]byte, error) { return []byte(raw.String()), nil }, time.Now()); err != nil {
		t.Fatal(err)
	}
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(method, target, host string) (int, string, http.Header) {
		req := httptest.NewRequest(method, target, nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String(), w.Header()
	}
	code, body, hdr := get("GET", "/", "127.0.0.1:7461")
	if code != 200 || !strings.Contains(body, `href="/project?p=www"`) || !strings.Contains(body, `href="/project?p=api"`) ||
		!strings.Contains(body, "box-b") || !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("portfolio %d:\n%s", code, body)
	}
	if strings.Contains(body, "<script") || regexp.MustCompile(`(src|href)="(https?:)?//`).MatchString(body) {
		t.Fatal("no script and nothing from another origin")
	}
	_, body, _ = get("GET", "/?machine=box-b", "localhost:7461")
	if strings.Contains(body, `href="/project?machine=box-b&amp;p=www"`) || !strings.Contains(body, `href="/project?machine=box-b&amp;p=api"`) {
		t.Fatalf("machine filter:\n%s", body)
	}
	_, body, _ = get("GET", "/project?p=www", "127.0.0.1")
	if !strings.Contains(body, `href="/goal?g=G-1&amp;p=www"`) {
		t.Fatalf("project:\n%s", body)
	}
	_, body, _ = get("GET", "/goal?g=G-1&p=www", "127.0.0.1")
	if !strings.Contains(body, `href="/task?k=box-a%2FT-1"`) || !strings.Contains(body, "FAIL: gate") {
		t.Fatalf("goal:\n%s", body)
	}
	_, body, _ = get("GET", "/task?k=box-a%2FT-1", "127.0.0.1")
	for _, want := range []string{"judge.verdict", "ci.red", `href="/session?s=s-judge-1"`, "$2.50", TraceID("box-a/T-1"),
		`href="https://github.com/zz-test/www/pull/7"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("task page lacks %q:\n%s", want, body)
		}
	}
	_, body, _ = get("GET", "/session?s=s-b-live", "127.0.0.1")
	if !strings.Contains(body, "pending") || !strings.Contains(body, "codex") {
		t.Fatalf("session:\n%s", body)
	}
	code, body, _ = get("GET", "/api/portfolio?harness=codex", "127.0.0.1")
	var api struct {
		Projects []ProjectLine `json:"projects"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil || len(api.Projects) != 2 || api.Projects[0].Project != "api" {
		t.Fatalf("api %d: %s", code, body)
	}
	if code, _, _ := get("POST", "/", "127.0.0.1"); code != 405 {
		t.Fatalf("POST: %d", code)
	}
	if code, _, _ := get("GET", "/", "evil.example:7461"); code != 403 {
		t.Fatalf("a foreign Host header: %d", code)
	}
	if code, _, _ := get("GET", "/task?k=nope", "127.0.0.1"); code != 404 {
		t.Fatalf("missing task: %d", code)
	}
	for _, a := range []string{"0.0.0.0:7461", "192.0.2.1:7461", ":7461"} {
		if CheckLoopback(a) == nil {
			t.Errorf("%s accepted", a)
		}
	}
	if CheckLoopback(ObsAddr(7461)) != nil || CheckLoopback("[::1]:1") != nil {
		t.Error("loopback refused")
	}
}

func TestAFailedRunRecordsItsSessionCost(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.AddSession(Session{SID: "s-1", Task: "T-0001", Role: "task", Model: "claude-sonnet-5-5", Started: nowISO()})
	usage := map[string]any{"input_tokens": 1000, "output_tokens": 2000, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
	line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "requestId": "q1",
		"message": map[string]any{"id": "m1", "model": "claude-sonnet-5-5", "usage": usage}})
	dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "p")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "s-1.jsonl"), append(line, '\n'), 0o600)
	r.fail(&Run{Task: "T-0001", Goal: "G-001", Project: "www"}, "test")
	evs, _ := r.Log.Read()
	var ended *Event
	for i := range evs {
		if evs[i].Event == "session.ended" {
			ended = &evs[i]
		}
	}
	u, ok := SessUsage{}, false
	if ended != nil {
		u, ok = usageOf(*ended)
	}
	if !ok || u.Input != 1000 || u.Output != 2000 || u.USD <= 0 || ended.Machine == "" || evs[len(evs)-1].Event != "task.failed" {
		t.Fatalf("session.ended of a failed run: %+v %+v", ended, u)
	}
}

func TestAPulledLogKeepsOneMachineName(t *testing.T) {
	r, _ := obsRunner(t)
	day := "2026-11-21"
	old := ev(day+"T01:00:00Z", "task.dispatched", "T-5", "G-5", "api", "", "", nil) // written before the machine field
	newer := ev(day+"T02:00:00Z", "pr.merged", "T-5", "G-5", "api", "", "", nil)
	newer.Machine = "box-b-host" // the remote's own machine_name, not the ssh alias
	red := ev(day+"T01:30:00Z", "ci.red", "T-5", "G-5", "api", "", "", nil)
	var raw strings.Builder
	for _, e := range []Event{old, red, red, newer} { // the same line twice in one file is two red rounds
		b, _ := json.Marshal(e)
		raw.Write(append(b, '\n'))
	}
	if _, _, err := r.ObsPull("alias-b:", "", func(string, string) ([]byte, error) { return []byte(raw.String()), nil }, time.Now()); err != nil {
		t.Fatal(err)
	}
	evs, err := LoadObsEvents(r.ObsSources(true))
	if err != nil {
		t.Fatal(err)
	}
	o := BuildObs(evs, ObsFilter{}, "box-a", nil, time.Now())
	x := o.Tasks["box-b-host/T-5"]
	if x == nil || o.Tasks["alias-b/T-5"] != nil || x.Row.Outcome != "merged" || x.Row.CIRounds != 2 {
		t.Fatalf("one machine, one task key: %v", o.TaskList("", ""))
	}
}

func TestOpenAsksCountPulledMachinesFromEvents(t *testing.T) {
	day := "2026-11-22T0"
	a := func(h, kind, m, p string, id float64) Event {
		e := ev(day+h+":00:00Z", kind, "", "", p, "", "", map[string]any{"id": id})
		e.Machine = m
		return e
	}
	evs := []Event{a("1", "ask.added", "box-b", "api", 1), a("2", "ask.added", "box-b", "api", 2), a("3", "ask.answered", "box-b", "api", 1),
		a("4", "ask.added", "box-c", "api", 1), a("5", "ask.withdrawn", "box-c", "api", 1), a("6", "ask.added", "box-a", "www", 1)}
	o := BuildObs(evs, ObsFilter{}, "box-a", nil, time.Now())
	got := o.OpenAsks("box-a", map[string]int{"www": 3}, true) // this machine: its DB wins over its events
	if got["api"] != 1 || got["www"] != 3 {
		t.Fatalf("open asks: %v", got)
	}
	if got := o.OpenAsks("box-a", nil, false); got["www"] != 1 {
		t.Fatalf("no local DB: events for every machine: %v", got)
	}
}

// stampedBoxA is boxA written as this machine's own log (every line carries machine box-a), with a transcript for
// every session so SessionUsages prices them, as the report does.
func stampedBoxA(t *testing.T, r *Runner, day string) {
	t.Helper()
	evs := boxA(day)
	for i := range evs {
		evs[i].Machine = "box-a"
		evs[i].CostUSD = nil
	}
	var kept []Event
	for _, e := range evs {
		if e.Event != "session.ended" { // priced from the transcripts, like a live session
			kept = append(kept, e)
		}
	}
	writeEvents(t, r.Log.Path, kept...)
	dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "p")
	_ = os.MkdirAll(dir, 0o700)
	for i, sid := range []string{"s-task-1", "s-judge-1", "s-task-2", "s-plan"} {
		usage := map[string]any{"input_tokens": 100000 * (i + 1), "output_tokens": 20000 * (i + 1), "cache_read_input_tokens": 50000,
			"cache_creation_input_tokens": 1000}
		line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"requestId": "q-" + sid, "message": map[string]any{"id": "m-" + sid, "model": "claude-sonnet-5-5", "usage": usage}})
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func obsGet(t *testing.T, h http.Handler, target string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Host = "127.0.0.1:7461"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func withinPct(a, b, pct float64) bool {
	if b == 0 {
		return a == 0
	}
	d := (a - b) / b
	if d < 0 {
		d = -d
	}
	return d <= pct/100
}

func TestObsSpendMatchesReport(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	day := time.Now().UTC().Format("2006-01-02")
	stampedBoxA(t, r, day)
	rows, err := r.ReportRows()
	if err != nil {
		t.Fatal(err)
	}
	var report float64 // what hivemill report prints as the cost of the tasks
	for _, x := range rows {
		report += x.USD
	}
	if report <= 0 {
		t.Fatalf("the report priced nothing: %+v", rows)
	}
	planner := SessionUsages(r.Env.UserHome, []string{"s-plan"})["s-plan"].USD
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	code, body := obsGet(t, h, "/api/obs/spend?by=task")
	var api struct {
		Spend SpendPage `json:"spend"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil {
		t.Fatalf("api %d: %s", code, body)
	}
	var tasks float64
	for _, row := range api.Spend.Rows {
		if row.TaskKey != "" {
			tasks += row.USD
		}
	}
	if !withinPct(tasks, report, 1) {
		t.Fatalf("task spend %.4f, report %.4f", tasks, report)
	}
	if !withinPct(api.Spend.Total, report+planner, 1) {
		t.Fatalf("total %.4f, report %.4f + planner %.4f", api.Spend.Total, report, planner)
	}
	var daily, byRole float64
	for _, d := range api.Spend.Daily {
		daily += d.Task + d.Judge + d.Planner
		if d.Planner > 0 {
			byRole += d.Planner
		}
	}
	if !withinPct(daily, api.Spend.Total, 1) || !withinPct(byRole, planner, 1) {
		t.Fatalf("the daily chart (%.4f, planner %.4f) differs from the table (%.4f, planner %.4f)", daily, byRole, api.Spend.Total, planner)
	}
	for _, by := range SpendGroups { // every grouping sums to the same total
		_, body := obsGet(t, h, "/api/obs/spend?by="+by)
		var g struct {
			Spend SpendPage `json:"spend"`
		}
		if json.Unmarshal([]byte(body), &g) != nil || g.Spend.By != by || !withinPct(g.Spend.Total, api.Spend.Total, 0.01) || len(g.Spend.Rows) == 0 {
			t.Fatalf("by %s: %s", by, body)
		}
		var sum float64
		for _, row := range g.Spend.Rows {
			sum += row.USD
		}
		if !withinPct(sum, g.Spend.Total, 0.01) {
			t.Fatalf("by %s: rows sum %.4f, total %.4f", by, sum, g.Spend.Total)
		}
	}
	if _, page := obsGet(t, h, "/spend?by=task"); !strings.Contains(page, `href="/task?k=box-a%2FT-1"`) {
		t.Fatalf("a task row opens the task:\n%s", page)
	}
}

func TestObsSpendUnattributed(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	ts := time.Now().UTC().Format("2006-01-02") + "T00:00:00Z"
	writeEvents(t, r.Log.Path,
		cost(ev(ts, "session.ended", "T-1", "G-1", "www", "s-task", "task", nil), 3.00, 1, 2),
		cost(ev(ts, "session.ended", "", "", "", "s-plan", "planner", nil), 1.00, 1, 2),
	)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	code, body := obsGet(t, h, "/api/obs/spend")
	var api struct {
		Spend map[string]any `json:"spend"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil {
		t.Fatalf("api %d: %s", code, body)
	}
	if api.Spend["usd"] != 4.0 || api.Spend["unattributed_usd"] != 1.0 || api.Spend["unattributed_pct"] != 25.0 {
		t.Fatalf("unattributed: %s", body)
	}
	row := regexp.MustCompile(`of which unattributed \(no task\): 25\.0%</td>(<td></td>)+<td class="num">\$1\.00</td>`)
	for _, by := range SpendGroups {
		if _, page := obsGet(t, h, "/spend?by="+by); !row.MatchString(page) {
			t.Fatalf("by %s: no unattributed row:\n%s", by, page)
		}
	}
	// A session with no task and no cost yet is marked, so the share is not read as complete.
	writeEvents(t, r.Log.Path, ev(ts, "session.started", "", "", "", "s-live", "planner", nil))
	if _, page := obsGet(t, h, "/spend"); !strings.Contains(page, `$1.00 <span class="warn">+1 pending</span></td><td colspan="4">`) {
		t.Fatalf("no pending marker:\n%s", page)
	}
}

func TestObsFlowStages(t *testing.T) {
	day := "2026-11-20"
	evs := boxA(day)
	ts := func(h string) string { return day + "T" + h + ":00Z" }
	evs = append(evs,
		ev(ts("04:00"), "task.dispatched", "T-4", "G-1", "www", "", "", nil), // 2.5 h in dispatched: stuck
		ev(ts("06:00"), "task.dispatched", "T-5", "G-1", "www", "", "", nil), // 30 min: not stuck
	)
	for i := range evs {
		evs[i].Machine = "box-a"
	}
	now, _ := time.Parse(time.RFC3339, ts("06:30"))
	o := BuildObs(evs, ObsFilter{}, "box-a", nil, now)
	f := o.BuildFlow(30, "")
	by := map[string]FlowTask{}
	for _, x := range f.Tasks {
		by[x.Task] = x
	}
	t1 := by["T-1"]
	want := map[string]int64{"dispatched": 2400, "gate": 0, "ci": 1800, "judge": 600, "merge": 2400}
	for st, sec := range want {
		if t1.Seconds[st] != sec {
			t.Errorf("T-1 %s: %d s, want %d (%+v)", st, t1.Seconds[st], sec, t1.Seconds)
		}
	}
	if t1.Stage != "" || t1.Total != 7200 {
		t.Errorf("T-1 finished in 2 h: %+v", t1)
	}
	if by["T-2"].Seconds["dispatched"] != 3600 || by["T-2"].Stage != "" {
		t.Errorf("T-2: %+v", by["T-2"])
	}
	if by["T-4"].Stage != "dispatched" || by["T-4"].AgeSec != 9000 || by["T-5"].AgeSec != 1800 {
		t.Errorf("open runs: %+v %+v", by["T-4"], by["T-5"])
	}
	if len(f.Stuck) != 1 || f.Stuck[0].Task != "T-4" {
		t.Errorf("stuck now: %+v", f.Stuck)
	}
	if f.Merged != 1 || f.MergedByDay[len(f.MergedByDay)-1].Day != day || f.MergedByDay[len(f.MergedByDay)-1].N != 1 {
		t.Errorf("merged per day: %+v", f.MergedByDay)
	}
	if !strings.Contains(string(f.Chart), "<svg") || !strings.Contains(string(f.Chart), `class="tick"`) {
		t.Error("the merged chart has axes")
	}
}

func TestObsPagesRender(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	day := time.Now().UTC().Format("2006-01-02")
	stampedBoxA(t, r, day)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	ext := regexp.MustCompile(`https?://|(src|href)="//`)
	for _, path := range []string{"/", "/spend", "/flow"} {
		for _, theme := range []string{"", "light", "dark"} {
			target := path
			if theme != "" {
				target += "?theme=" + theme
			}
			code, body := obsGet(t, h, target)
			if code != 200 || strings.Contains(body, "<svg") == (path == "/") || !strings.Contains(body, `class="on"`) {
				t.Fatalf("%s: %d\n%s", target, code, body)
			}
			if theme != "" && !strings.Contains(body, `data-theme="`+theme+`"`) || theme == "" && strings.Contains(body, "data-theme") {
				t.Errorf("%s: theme attribute", target)
			}
			if ext.MatchString(body) || strings.Contains(body, "<script") {
				t.Errorf("%s: an external URL or a script", target)
			}
		}
	}
	_, css := obsGet(t, h, "/assets/obs/app.css")
	for _, want := range []string{`[data-theme="dark"]`, "prefers-color-scheme: dark", "--honey"} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
	if ext.MatchString(css) || strings.Contains(css, "box-shadow") {
		t.Error("the stylesheet loads nothing and has no shadow")
	}
	for _, path := range []string{"/api/obs/now", "/api/obs/spend", "/api/obs/flow"} {
		code, body := obsGet(t, h, path)
		var v map[string]any
		if code != 200 || json.Unmarshal([]byte(body), &v) != nil || v["now"] == nil {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	_, body := obsGet(t, h, "/flow?project=www")
	if !strings.Contains(body, "T-1") {
		t.Errorf("project filter:\n%s", body)
	}
}

// pollingFixture is a running obs page over one task and one judge session with the given $ and polling $.
func pollingFixture(t *testing.T, taskUSD, taskPolling, judgeUSD, judgePolling float64) (*Runner, http.Handler) {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	ts := time.Now().UTC().Format("2006-01-02") + "T00:00:00Z"
	ended := func(sid, role string, usd, polling float64) Event {
		e := cost(ev(ts, "session.ended", "T-1", "G-1", "www", sid, role, nil), usd, 1, 2)
		e.Data["polling_usd"] = polling
		return e
	}
	writeEvents(t, r.Log.Path, ended("s-task", "task", taskUSD, taskPolling), ended("s-judge", "judge", judgeUSD, judgePolling))
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	return r, h
}

var bannerHTML = regexp.MustCompile(`(?s)<p class="banner">.*?</p>`)

// spendBanners is the API's page model and the banner elements of the HTML page.
func spendBanners(t *testing.T, h http.Handler) (SpendPage, []string) {
	t.Helper()
	code, body := obsGet(t, h, "/api/obs/spend")
	var api struct {
		Spend SpendPage `json:"spend"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil || !strings.Contains(body, `"banners":[`) {
		t.Fatalf("api %d: %s", code, body)
	}
	code, page := obsGet(t, h, "/spend")
	if code != 200 {
		t.Fatalf("page %d: %s", code, page)
	}
	return api.Spend, bannerHTML.FindAllString(page, -1)
}

func TestObsBannerPolling(t *testing.T) {
	t.Run("below", func(t *testing.T) {
		_, h := pollingFixture(t, 8, 1.6, 2, 0.4) // exactly 20% everywhere: not above
		p, html := spendBanners(t, h)
		if p.PollingUSD != 2 || p.PollingPct != 20 || len(p.Banners) != 0 || len(html) != 0 {
			t.Fatalf("no banner at the threshold: %+v %q", p, html)
		}
	})
	t.Run("above", func(t *testing.T) {
		_, h := pollingFixture(t, 8, 3, 2, 0.6) // 36%
		p, html := spendBanners(t, h)
		want := "Polling is 36% of spend ($3.60). Action: replace wait turns with one blocking command."
		if len(p.Banners) != 1 || p.Banners[0] != want || len(html) != 1 || !strings.Contains(html[0], want) {
			t.Fatalf("one banner: %q %q", p.Banners, html)
		}
		if strings.Contains(html[0], "<button") || strings.Contains(html[0], "<form") || strings.Contains(html[0], "<a ") {
			t.Fatalf("a banner is text only: %s", html[0])
		}
	})
}

func TestObsBannerPollingRole(t *testing.T) {
	_, h := pollingFixture(t, 9, 0.9, 1, 0.5) // factory 14%, judge 50%
	p, html := spendBanners(t, h)
	want := "Polling is 50% of judge spend ($0.50). Action: replace wait turns with one blocking command."
	if len(p.Banners) != 1 || p.Banners[0] != want || len(html) != 1 {
		t.Fatalf("the role banner: %q %q", p.Banners, html)
	}
	roles := []SpendRole{{"task", 9, 0.9, 10}, {"judge", 1, 0.5, 50}}
	if !reflect.DeepEqual(p.Roles, roles) {
		t.Fatalf("roles: %+v", p.Roles)
	}
}

func TestObsBannerPollingJustAbove(t *testing.T) {
	_, h := pollingFixture(t, 8, 1.62, 2, 0.4) // 20.2%
	if p, _ := spendBanners(t, h); len(p.Banners) != 1 || !strings.HasPrefix(p.Banners[0], "Polling is 20.2% of spend") {
		t.Fatalf("the printed share reads above the threshold: %q", p.Banners)
	}
	_, h = pollingFixture(t, 160, 33, 40, 8) // 20.5% rounds up, never to the even 20
	if p, _ := spendBanners(t, h); len(p.Banners) != 1 || !strings.HasPrefix(p.Banners[0], "Polling is 21% of spend") {
		t.Fatalf("20.5%%: %q", p.Banners)
	}
}

func TestObsBannerPollingConfig(t *testing.T) {
	r, h := pollingFixture(t, 8, 3, 2, 0.6)
	if err := r.Store.SetCfg("obs_banner_polling_pct", "50"); err != nil {
		t.Fatal(err)
	}
	if p, html := spendBanners(t, h); len(p.Banners) != 0 || len(html) != 0 {
		t.Fatalf("36%% is under the configured 50%%: %q %q", p.Banners, html)
	}
}

// roundsFixture is a running obs page over four PRs, the first many of which needed 2 review rounds.
func roundsFixture(t *testing.T, many int) (*Runner, http.Handler) {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	ts := time.Now().UTC().Format("2006-01-02") + "T00:00:00Z"
	e := func(kind, task string, data map[string]any) Event {
		return ev(ts, kind, task, "G-1", "www", "", "", data)
	}
	// T-5 has no PR, so it is not counted, whatever its rounds.
	evs := []Event{e("ci.red", "T-5", nil), e("ci.red", "T-5", nil)}
	for i := 1; i <= 4; i++ {
		task := fmt.Sprintf("T-%d", i)
		evs = append(evs, e("pr.merged", task, map[string]any{"pr": task}), e("judge.verdict", task, map[string]any{"verdict": "PASS"}))
		if i <= many {
			evs = append(evs, e("ci.red", task, nil))
		}
	}
	writeEvents(t, r.Log.Path, evs...)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	return r, h
}

// flowBanners is the API's Flow page model.
func flowBanners(t *testing.T, h http.Handler) FlowPage {
	t.Helper()
	code, body := obsGet(t, h, "/api/obs/flow")
	var api struct {
		Flow FlowPage `json:"flow"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &api) != nil || !strings.Contains(body, `"banners":[`) {
		t.Fatalf("api %d: %s", code, body)
	}
	return api.Flow
}

// flowBannerHTML is the banner elements of the Flow HTML page.
func flowBannerHTML(t *testing.T, h http.Handler) []string {
	t.Helper()
	code, page := obsGet(t, h, "/flow")
	if code != 200 {
		t.Fatalf("page %d: %s", code, page)
	}
	return bannerHTML.FindAllString(page, -1)
}

func TestObsBannerRounds(t *testing.T) {
	t.Run("below", func(t *testing.T) {
		_, h := roundsFixture(t, 1) // 25%
		if p := flowBanners(t, h); p.PRs != 4 || p.Rounds2 != 1 || len(p.Banners) != 0 {
			t.Fatalf("no banner: %+v", p)
		}
	})
	t.Run("above", func(t *testing.T) {
		_, h := roundsFixture(t, 2)
		want := []string{"2 of 4 PRs (50%) needed 2+ review rounds."}
		if p := flowBanners(t, h); p.PRs != 4 || p.Rounds2 != 2 || !reflect.DeepEqual(p.Banners, want) {
			t.Fatalf("one banner: %+v", p)
		}
	})
}

func TestObsBannerRoundsConfig(t *testing.T) {
	r, h := roundsFixture(t, 2)
	if err := r.Store.SetCfg("obs_banner_rounds_pct", "60"); err != nil {
		t.Fatal(err)
	}
	if p := flowBanners(t, h); len(p.Banners) != 0 {
		t.Fatalf("50%% is under the configured 60%%: %q", p.Banners)
	}
}

func TestObsFlowBannerHTML(t *testing.T) {
	t.Run("shown", func(t *testing.T) {
		_, h := roundsFixture(t, 2) // 50%
		html := flowBannerHTML(t, h)
		// html/template writes "+" as &#43;.
		want := "2 of 4 PRs (50%) needed 2&#43; review rounds."
		if len(html) != 1 || !strings.Contains(html[0], want) {
			t.Fatalf("one banner: %q", html)
		}
		if strings.Contains(html[0], "<button") || strings.Contains(html[0], "<form") || strings.Contains(html[0], "<a") {
			t.Fatalf("a banner is text only: %s", html[0])
		}
	})
	t.Run("hidden", func(t *testing.T) {
		_, h := roundsFixture(t, 1) // 25%
		if html := flowBannerHTML(t, h); len(html) != 0 {
			t.Fatalf("no banner: %q", html)
		}
	})
}

// stageFixture is a running obs page over one merged task with the given hours in dispatched, ci and judge, plus one
// hour each in gate and merge when even.
func stageFixture(t *testing.T, even bool, dispatched, ci, judge int) (*Runner, http.Handler) {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	at := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	var evs []Event
	add := func(kind, role string, data map[string]any, hours int) {
		evs = append(evs, ev(at.Format(time.RFC3339), kind, "T-1", "G-1", "www", "", role, data))
		at = at.Add(time.Duration(hours) * time.Hour)
	}
	add("task.dispatched", "", nil, dispatched)
	if even {
		add("gate.blocked", "", nil, 1)
	}
	add("gate.passed", "", nil, ci)
	add("session.started", "judge", nil, judge)
	if even {
		add("judge.verdict", "", map[string]any{"verdict": "MERGE"}, 1)
	}
	add("pr.merged", "", map[string]any{"pr": "1"}, 0)
	writeEvents(t, r.Log.Path, evs...)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	return r, h
}

const stageWant = "ci holds 3 of 5 stage hours (60%)."

func TestObsBannerStage(t *testing.T) {
	t.Run("below", func(t *testing.T) {
		_, h := stageFixture(t, true, 1, 1, 1) // five stages of 20%
		if p := flowBanners(t, h); len(p.Tasks) != 1 || p.Tasks[0].Total != 5*3600 || len(p.Banners) != 0 {
			t.Fatalf("no banner: %+v", p)
		}
		at := &FlowPage{Tasks: []FlowTask{{Seconds: map[string]int64{"ci": 30, "gate": 30, "judge": 20, "merge": 20}}}}
		lone := &FlowPage{Tasks: []FlowTask{{Seconds: map[string]int64{"ci": 3600, "gate": 0}}}}
		if a, l := at.stageBanners(30), lone.stageBanners(30); a != nil || l != nil {
			t.Fatalf("no banner at the threshold or for a lone stage: %q %q", a, l)
		}
	})
	t.Run("above", func(t *testing.T) {
		_, h := stageFixture(t, false, 1, 3, 1)
		if p := flowBanners(t, h); !reflect.DeepEqual(p.Banners, []string{stageWant}) {
			t.Fatalf("one banner: %+v", p)
		}
		html := flowBannerHTML(t, h)
		if len(html) != 1 || !strings.Contains(html[0], stageWant) {
			t.Fatalf("one banner element: %q", html)
		}
		if strings.Contains(html[0], "<button") || strings.Contains(html[0], "<form") || strings.Contains(html[0], "<a") {
			t.Fatalf("a banner is text only: %s", html[0])
		}
	})
}

func TestObsBannerStageConfig(t *testing.T) {
	r, h := stageFixture(t, false, 1, 3, 1)
	if err := r.Store.SetCfg("obs_banner_stage_pct", "90"); err != nil {
		t.Fatal(err)
	}
	if p := flowBanners(t, h); len(p.Banners) != 0 {
		t.Fatalf("60%% is under the configured 90%%: %q", p.Banners)
	}
}

func TestSessionPollingUSD(t *testing.T) {
	r, _ := testRunner(t, "")
	dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "p")
	_ = os.MkdirAll(dir, 0o700)
	var raw []byte
	for i, out := range []int{2000, 10} { // the second request is a wait turn
		usage := map[string]any{"input_tokens": 1000, "output_tokens": out, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
		id := string(rune('a' + i))
		line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"requestId": "q-" + id, "message": map[string]any{"id": "m-" + id, "model": "claude-sonnet-5-5", "usage": usage}})
		raw = append(raw, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "s-1.jsonl"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	u := SessionUsages(r.Env.UserHome, []string{"s-1"})["s-1"]
	if u.PollingUSD <= 0 || u.PollingUSD >= u.USD {
		t.Fatalf("polling part: %+v", u)
	}
	b, _ := json.Marshal(Event{CostUSD: &u.USD, Data: u.data()})
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	if got, ok := usageOf(e); !ok || got != u {
		t.Fatalf("session.ended round trip: %+v, want %+v", got, u)
	}
	delete(e.Data, "polling_usd") // a line from before the field
	if got, ok := usageOf(e); !ok || got.PollingUSD != 0 {
		t.Fatalf("old line: %+v", got)
	}
}
