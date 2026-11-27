package hm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// summaryRunner is a factory with one run working, one in CI, one long in merge (stuck), one held with a reason and
// a PR, one failed an hour ago, and one open owner question.
func summaryRunner(t *testing.T) (*Runner, *SQLite) {
	t.Helper()
	r, s := testRunner(t, "")
	r.Log.Machine = "box-a"
	now := time.Now().UTC()
	at := func(minAgo int) string { return now.Add(-time.Duration(minAgo) * time.Minute).Format(time.RFC3339) }
	pr := "https://github.com/zz-test/www/pull/7"
	writeEvents(t, r.Log.Path,
		ev(at(20), "task.dispatched", "T-1", "G-1", "www", "", "", nil),
		ev(at(50), "task.dispatched", "T-2", "G-1", "www", "", "", nil),
		ev(at(40), "gate.passed", "T-2", "G-1", "www", "", "", nil),
		ev(at(400), "task.dispatched", "T-3", "G-1", "www", "", "", nil),
		ev(at(390), "gate.passed", "T-3", "G-1", "www", "", "", nil),
		ev(at(380), "judge.verdict", "T-3", "G-1", "www", "s-j3", "judge", map[string]any{"verdict": "MERGE"}),
		ev(at(90), "task.dispatched", "T-4", "G-1", "www", "", "", nil),
		ev(at(32), "run.held", "T-4", "G-1", "www", "", "", map[string]any{"why": "tier T3 is above T2; the owner merges", "pr": pr}),
		ev(at(120), "task.dispatched", "T-5", "G-1", "www", "", "", nil),
		ev(at(60), "task.failed", "T-5", "G-1", "www", "", "", map[string]any{"reason": "stalled: the task session stayed idle"}),
	)
	if _, _, err := s.AddAsk(Ask{Project: "www", Task: "T-1", Question: "Ship with the old colours?", AskedBy: "planner", Asked: at(15)}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetCfg("obs_label", "Workshop")
	_ = s.SetCfg("obs_machine_labels", "box-a=Bench 1, box-z=Bench 9")
	_ = s.SetMeta("planner", "on")
	return r, s
}

func getSummary(t *testing.T, r *Runner) (*Summary, string) {
	t.Helper()
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/obs/v1/summary", nil)
	req.Host = "127.0.0.1:7461"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("summary %d: %s", w.Code, w.Body.String())
	}
	var sum Summary
	if err := json.Unmarshal(w.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	return &sum, w.Body.String()
}

func TestSummaryCountsNeedsAndIdentity(t *testing.T) {
	r, _ := summaryRunner(t)
	sum, body := getSummary(t, r)
	if sum.API != ObsAPI || sum.Runner.Label != "Workshop" || !regexp.MustCompile(`^r-[0-9a-f]{12}$`).MatchString(sum.Runner.ID) || sum.Runner.Version == "" {
		t.Fatalf("identity: %+v", sum.Runner)
	}
	if strings.Join(sum.Runner.Projects, ",") != "www" {
		t.Fatalf("projects: %v", sum.Runner.Projects)
	}
	if sum.Counts != (SummaryCounts{Working: 1, CI: 1, Merge: 1, Held: 1}) {
		t.Fatalf("counts: %+v", sum.Counts)
	}
	var kinds []string
	by := map[string]Need{}
	for _, n := range sum.NeedsYou {
		kinds = append(kinds, n.Kind)
		by[n.Kind] = n
	}
	if strings.Join(kinds, ",") != "question,held,failed,stuck" {
		t.Fatalf("needs in order question, held, failed, stuck: %v", kinds)
	}
	if sum.NeedsCount != 2 || sum.NeedKinds["question"] != 1 || sum.NeedKinds["held"] != 1 || sum.NeedKinds["failed"] != 1 || sum.NeedKinds["stuck"] != 1 {
		t.Fatalf("only questions and held runs count: %d %v", sum.NeedsCount, sum.NeedKinds)
	}
	if q := by["question"]; q.Text != "Ship with the old colours?" || !q.Act || q.Project != "www" || q.Task != "T-1" {
		t.Fatalf("question: %+v", q)
	}
	held := by["held"]
	if held.Text != "tier T3 is above T2; the owner merges" || held.PR != "https://github.com/zz-test/www/pull/7" || held.Href != held.PR || held.Task != "T-4" || !held.Act {
		t.Fatalf("held: %+v", held)
	}
	if f := by["failed"]; f.Task != "T-5" || f.Act || !strings.Contains(f.Text, "stalled") || f.Href != "/task?k=box-a%2FT-5" {
		t.Fatalf("failed: %+v", f)
	}
	if st := by["stuck"]; st.Task != "T-3" || !strings.HasPrefix(st.Text, "merge for 6h") || !strings.HasSuffix(st.Text, "limit 2h 00m") {
		t.Fatalf("stuck: %+v", st)
	}
	if !sum.Planner.On || len(sum.Merged.PerDay) != 7 || sum.Budget.TaskCapUSD == nil || sum.Budget.WeeklyCapUSD != nil {
		t.Fatalf("planner, merged, budget: %+v %+v %+v", sum.Planner, sum.Merged, sum.Budget)
	}
	if len(sum.Machines) != 1 || sum.Machines[0].Name != "box-a" || sum.Machines[0].Label != "Bench 1" || !sum.Machines[0].Coordinator {
		t.Fatalf("machines: %+v", sum.Machines)
	}
	if strings.Contains(body, r.Env.Home) || strings.Contains(body, "events.jsonl") {
		t.Fatal("the summary names no path")
	}
}

func TestSummaryLeavesOutARefiledFailureAndFlagsThePlanner(t *testing.T) {
	r, s := summaryRunner(t)
	if _, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,status,acceptance) VALUES('T-6','G-001','again (re-file of T-5)','spec','todo','1. x. cmd:true => exit 0')`); err != nil {
		t.Fatal(err)
	}
	_ = s.SetMeta("planner", "off")
	_ = s.SetCfg("obs_stuck_merge_min", "600")
	_ = s.SetCfg("weekly_budget_usd", "0.01")
	sum, _ := getSummary(t, r)
	for _, n := range sum.NeedsYou {
		if n.Kind == "failed" || n.Kind == "stuck" {
			t.Fatalf("a re-filed failure and a run under its stage limit are not listed: %+v", n)
		}
	}
	if sum.NeedKinds["system"] != 1 || sum.NeedsYou[len(sum.NeedsYou)-1].Text != "the planner is off" || sum.NeedsCount != 2 {
		t.Fatalf("system rows: %+v", sum.NeedsYou)
	}
	if sum.Budget.WeeklyCapUSD == nil || *sum.Budget.WeeklyCapUSD != 0.01 {
		t.Fatalf("weekly cap: %+v", sum.Budget)
	}
}

func TestSummaryIsCachedBetweenClients(t *testing.T) {
	r, s := summaryRunner(t)
	h, _ := r.ObsHandler()
	get := func() string {
		req := httptest.NewRequest("GET", "/api/obs/v1/summary", nil)
		req.Host = "localhost:7461"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Body.String()
	}
	a := get()
	_ = s.SetCfg("obs_label", "Renamed")
	if b := get(); a != b {
		t.Fatal("a second client within the TTL gets the same answer")
	}
	h.(*obsServer).now = func() time.Time { return time.Now().Add(2 * summaryTTL) }
	if !strings.Contains(get(), `"label":"Renamed"`) {
		t.Fatal("after the TTL the summary is rebuilt")
	}
}

func TestWideServerNamesNoPathOrPullSource(t *testing.T) {
	r, day := obsRunner(t)
	var raw strings.Builder
	for _, e := range boxB(day) {
		b, _ := json.Marshal(e)
		raw.Write(append(b, '\n'))
	}
	if _, _, err := r.ObsPull("box-b:x", "", func(string, string) ([]byte, error) { return []byte(raw.String()), nil }, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, wide := range []bool{false, true} {
		h, err := r.obsHandler(wide)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/obs/now", "/api/portfolio", "/"} {
			req := httptest.NewRequest("GET", path, nil)
			req.Host = map[bool]string{false: "127.0.0.1:7461", true: "192.0.2.10:7461"}[wide]
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			leaks := strings.Contains(w.Body.String(), r.Env.Home) || strings.Contains(w.Body.String(), "box-b:x")
			if w.Code != 200 || (wide && leaks) {
				t.Fatalf("wide=%v %s: %d, leaks=%v", wide, path, w.Code, leaks)
			}
			if wide && path == "/" && !strings.Contains(w.Body.String(), "pulled") {
				t.Fatal("a pulled machine is still told apart")
			}
		}
	}
}

func TestParsePeers(t *testing.T) {
	got, err := ParsePeers(" http://192.0.2.10:7461/ , https://192.0.2.11:7461, http://192.0.2.10:7461 ")
	if err != nil || strings.Join(got, " ") != "http://192.0.2.10:7461 https://192.0.2.11:7461" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := ParsePeers(""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, bad := range []string{"192.0.2.10:7461", "ftp://192.0.2.10", "http://user:pw" + "\x40" + "192.0.2.10", "http://192.0.2.10/x", "http://192.0.2.10/?a=b", "http://"} {
		if _, err := ParsePeers(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func peerServer(t *testing.T, label string, hits *int32, mod func(*Summary)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if req.URL.Path != "/api/obs/v1/summary" {
			http.NotFound(w, req)
			return
		}
		sum := Summary{API: 1, Now: time.Now(), Runner: RunnerInfo{ID: "r-" + label, Label: label}, Counts: SummaryCounts{Working: 2, Held: 1}, NeedsCount: 1,
			NeedsYou: []Need{{Kind: "held", Act: true, Task: "T-9", Key: "m/T-9", Text: "the owner merges", AgeSec: 600, PR: "https://example.com/pr/9", Href: "https://example.com/pr/9"}},
			Machines: []SummaryMachine{{Name: "m", Label: "Bench", Down: true}}}
		if mod != nil {
			mod(&sum)
		}
		_ = json.NewEncoder(w).Encode(sum)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHubKeepsTheLastGoodAnswerAndMarksEachPeer(t *testing.T) {
	var hits int32
	ok := peerServer(t, "Pilot", &hits, nil)
	old := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(old.Close)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()

	hub := NewHub([]string{ok.URL, old.URL, slow.URL, goneURL})
	hub.timeout = 150 * time.Millisecond
	clock := time.Now()
	hub.now = func() time.Time { return clock }
	start := time.Now()
	got := hub.Factories(context.Background(), &Summary{API: 1, Now: clock, Runner: RunnerInfo{Label: "Workshop"}})
	if took := time.Since(start); took > time.Second {
		t.Fatalf("peers are asked at once and a slow one is cut off: %s", took)
	}
	if len(got) != 5 || !got[0].Self || got[0].Summary.Runner.Label != "Workshop" {
		t.Fatalf("the serving factory comes first: %+v", got[0])
	}
	if p := got[1]; p.URL != ok.URL || p.Stale || p.Error != "" || p.Summary == nil || p.Summary.Runner.Label != "Pilot" {
		t.Fatalf("ok peer: %+v", p)
	}
	for i, want := range map[int]string{2: "no API v1: update this factory", 3: "no answer in time", 4: "unreachable"} {
		if p := got[i]; p.Error != want || !p.Stale || p.Summary != nil {
			t.Fatalf("peer %d: %+v, want error %q", i, p, want)
		}
	}
	// within the TTL nobody is asked again
	hub.Factories(context.Background(), nil)
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("asked %d times within the TTL", n)
	}
	// the peer goes away: its last answer stays, marked with the error and its age
	ok.Close()
	clock = clock.Add(2 * time.Minute)
	p := hub.Factories(context.Background(), nil)[0]
	if p.Summary == nil || p.Summary.Runner.Label != "Pilot" || p.Error != "unreachable" || !p.Stale || p.AgeSec != 120 {
		t.Fatalf("last good answer kept: %+v", p)
	}
	page := BuildHubPage([]HubPeer{p})
	if page.Rows[0].State != "unreachable" || page.NeedsCount != 0 || len(page.Needs) != 1 || page.Needs[0].Kind != "system" {
		t.Fatalf("a factory that stopped answering lists only that: %+v", page)
	}
}

func TestHubPageOnAFactoryAndAlone(t *testing.T) {
	peer := peerServer(t, "Pilot", nil, nil)
	r, s := summaryRunner(t)
	h0, _ := r.ObsHandler()
	get := func(h http.Handler, method, target, host string) (int, string, http.Header) {
		req := httptest.NewRequest(method, target, nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String(), w.Header()
	}
	if code, body, _ := get(h0, "GET", "/all", "127.0.0.1:7461"); code != 404 || !strings.Contains(body, "obs_peers") {
		t.Fatalf("no peers, no hub page: %d", code)
	}
	if code, _, _ := get(h0, "GET", "/api/obs/v1/factories", "127.0.0.1:7461"); code != 404 {
		t.Fatalf("no peers, no factories API: %d", code)
	}
	if _, body, _ := get(h0, "GET", "/", "127.0.0.1:7461"); strings.Contains(body, "All factories") || !strings.Contains(body, `<span class="label">Workshop</span>`) {
		t.Fatal("without peers the shell has no hub link; the factory's label is in the brand")
	}

	_ = s.SetCfg("obs_peers", peer.URL)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	code, body, hdr := get(h, "GET", "/all", "127.0.0.1:7461")
	if code != 200 || !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") || strings.Contains(body, "<script") {
		t.Fatalf("hub page %d\n%s", code, body)
	}
	for _, want := range []string{
		`<a href="/">Workshop</a> <span class="muted">this one</span>`,
		`<a href="` + peer.URL + `/">Pilot</a>`,
		`<a href="` + peer.URL + `/task?k=m%2FT-9">T-9</a>`,
		`<a href="https://example.com/pr/9">PR</a>`,
		`<a href="https://github.com/zz-test/www/pull/7">PR</a>`,
		"Ship with the old colours?",
		`<span class="bad">1 down</span>`,
		`aria-current="page">All factories</a>`,
		"3 need you, over 2 factories.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hub page misses %q", want)
		}
	}
	if regexp.MustCompile(`(src)="(https?:)?//`).MatchString(body) {
		t.Fatal("the hub page loads nothing from another origin")
	}
	code, body, _ = get(h, "GET", "/api/obs/v1/factories", "localhost:7461")
	var fac struct {
		API       int       `json:"api"`
		Factories []HubPeer `json:"factories"`
	}
	if err := json.Unmarshal([]byte(body), &fac); err != nil || code != 200 || fac.API != 1 || len(fac.Factories) != 2 || !fac.Factories[0].Self || fac.Factories[1].URL != peer.URL {
		t.Fatalf("factories API %d: %s", code, body)
	}
	if _, body, _ := get(h, "GET", "/", "127.0.0.1:7461"); !strings.Contains(body, `<a href="/all">All factories</a>`) {
		t.Fatal("a factory with peers links its hub page")
	}

	// a factory that is not the hub links the hub it was told about
	_ = s.SetCfg("obs_peers", "")
	_ = s.SetCfg("obs_hub_url", "http://192.0.2.9:7461/")
	h2, _ := r.ObsHandler()
	if _, body, _ := get(h2, "GET", "/flow", "127.0.0.1:7461"); !strings.Contains(body, `<a href="http://192.0.2.9:7461/all">All factories</a>`) {
		t.Fatal("obs_hub_url gives the link back to the hub")
	}

	// a hub with no factory behind it
	alone, err := HubHandler(NewHub([]string{peer.URL}), false)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = get(alone, "GET", "/", "127.0.0.1:7461")
	if code != 200 || !strings.Contains(body, `<a href="`+peer.URL+`/">Pilot</a>`) || strings.Contains(body, `href="/spend"`) || !strings.Contains(body, "hivemill obs hub, read-only") {
		t.Fatalf("hub alone %d\n%s", code, body)
	}
	if code, _, hdr := get(alone, "POST", "/", "127.0.0.1:7461"); code != 405 || hdr.Get("Allow") != "GET, HEAD" {
		t.Fatalf("read-only: %d", code)
	}
	if code, _, _ := get(alone, "GET", "/", "example.com"); code != 403 {
		t.Fatalf("a DNS Host is refused: %d", code)
	}
	if code, body, _ := get(alone, "GET", "/spend", "127.0.0.1:7461"); code != 404 || !strings.Contains(body, "Back to All factories") {
		t.Fatalf("no factory pages on a hub alone: %d", code)
	}
	if code, body, _ := get(alone, "GET", "/api/obs/v1/factories", "127.0.0.1:7461"); code != 200 || !strings.Contains(body, `"label":"Pilot"`) {
		t.Fatalf("factories API alone: %d %s", code, body)
	}
}

func TestPeerHrefOnlyLinksConfiguredOriginsAndHTTPS(t *testing.T) {
	for _, c := range [][3]string{
		{"", "/task?k=a", "/task?k=a"},
		{"http://192.0.2.10:7461", "/flow", "http://192.0.2.10:7461/flow"},
		{"http://192.0.2.10:7461/", "/", "http://192.0.2.10:7461/"},
		{"http://192.0.2.10:7461", "https://example.com/pr/1", "https://example.com/pr/1"},
		{"http://192.0.2.10:7461", "//evil.example/x", "http://192.0.2.10:7461/"},
		{"http://192.0.2.10:7461", "javascript:alert(1)", "http://192.0.2.10:7461/"},
		{"javascript:alert(1)", "/x", ""},
	} {
		if got := string(peerHref(c[0], c[1])); got != c[2] {
			t.Errorf("peerHref(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
