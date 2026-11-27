package hm

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// `hivemill obs`: a read-only dashboard, on 127.0.0.1 unless --listen says otherwise. html/template pages and one embedded stylesheet, no script,
// nothing loaded from another origin. Sidebar: Now, Spend, Flow, Machines (obs_machines.go); the drill-down zoom (project -> goal -> task ->
// session) stays under Now. Header: project, time range, machine, harness, theme. JSON of every page: /api/obs/*.

//go:embed assets/obs
var obsFS embed.FS

// ObsAddr is the listen address of host and port.
func ObsAddr(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// ObsListen picks the bind host: --listen, else config obs_listen, else 127.0.0.1. It takes an IP literal or
// localhost, and warns on stderr when the host is not loopback.
func ObsListen(flagHost, cfgHost string, stderr io.Writer) (string, error) {
	host := strings.TrimSpace(or(strings.TrimSpace(flagHost), strings.TrimSpace(cfgHost)))
	if host == "" {
		host = "127.0.0.1"
	}
	if err := checkBindHost(host); err != nil {
		return "", err
	}
	if !loopbackHost(host) {
		fmt.Fprintf(stderr, "WARNING: hivemill obs listens on %s: the page is read-only but readable by anyone on that network\n", host)
	}
	return host, nil
}

// checkBindHost refuses a host that is not an IP literal or localhost.
func checkBindHost(h string) error {
	if h != "localhost" && net.ParseIP(h) == nil {
		return fmt.Errorf("obs listens on an IP address or localhost, not %q", h)
	}
	return nil
}

// CheckLoopback refuses an address that is not a loopback address.
func CheckLoopback(addr string) error {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if h == "localhost" {
		return nil
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("obs binds loopback only, not %q", h)
	}
	return nil
}

type obsServer struct {
	r      *Runner
	tmpl   *template.Template
	mu     sync.Mutex
	priced time.Time
	usage  map[string]SessUsage
	now    func() time.Time
	wide   bool // bound beyond loopback: an IP literal Host is answered too

	coreMu sync.Mutex
	coreAt time.Time // when hc was last asked
	core   string    // the last core name hc gave

	probeMu sync.Mutex
	probed  map[string]machineProbe // the Machines page's reading of each session machine

	sumMu sync.Mutex
	sumAt time.Time
	sum   *Summary // the last /api/obs/v1/summary, served for summaryTTL

	hub *Hub // set when config obs_peers lists other factories
}

// coreTTL is how long one `hc server status` answer, a failure too, serves the page.
const coreTTL = 30 * time.Second

// coreName is Runner.knownCoreName asked at most once per coreTTL. A failed call keeps the last name hc gave, so this
// machine is not listed a second time under it.
func (s *obsServer) coreName() string {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	if s.coreAt.IsZero() || s.now().Sub(s.coreAt) >= coreTTL {
		s.core = or(s.r.knownCoreName(), s.core)
		s.coreAt = s.now() // after the call: a hung hc can outlast coreTTL and must still be cached
	}
	return s.core
}

// usages prices this machine's sessions from their transcripts at most once a minute.
func (s *obsServer) usages(ids []string) map[string]SessUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	missing := false
	for _, id := range ids {
		if _, ok := s.usage[id]; !ok {
			missing = true
			break
		}
	}
	if s.usage == nil || time.Since(s.priced) > time.Minute || (missing && time.Since(s.priced) > 10*time.Second) {
		s.usage, s.priced = SessionUsages(s.r.Env.UserHome, ids), time.Now()
	}
	return s.usage
}

func (s *obsServer) load(f ObsFilter) (*Obs, []ObsSource, error) {
	srcs := s.r.ObsSources(true)
	evs, sources, err := loadObs(srcs)
	if err != nil {
		return nil, nil, err
	}
	me := s.r.Machine()
	var ids []string
	for _, e := range evs {
		if e.Session != "" && e.Machine == me {
			ids = append(ids, e.Session)
		}
	}
	o := BuildObs(evs, f, me, s.usages(uniq(ids)), s.now())
	s.r.listMachines(o, sources, s.coreName)
	listed := srcs[:0]
	for i, src := range srcs {
		if src.Machine = sources[i]; src.Machine != "" { // "": the lines' own machines are listed instead
			listed = append(listed, src)
		}
	}
	return o, listed, nil
}

// ObsHandler is the page. It answers GET and HEAD only, and only for a loopback Host (no DNS rebinding).
func (r *Runner) ObsHandler() (http.Handler, error) { return r.obsHandler(false) }

// obsHandler is ObsHandler; wide also answers a Host that is an IP literal (a DNS name stays refused).
func (r *Runner) obsHandler(wide bool) (http.Handler, error) {
	t, err := template.New("page.html").Funcs(obsFuncs).ParseFS(obsFS, "assets/obs/*.html")
	if err != nil {
		return nil, err
	}
	s := &obsServer{r: r, tmpl: t, now: time.Now, wide: wide}
	// A bad obs_peers entry is not fatal: the factory's own pages must still come up.
	if peers, err := ParsePeers(r.Store.Cfg("obs_peers")); err == nil && len(peers) > 0 {
		s.hub = NewHub(peers)
	}
	return s, nil
}

// obsSocket opens the listener for addr. An IPv4 literal gets "tcp4": on "tcp", Go turns 0.0.0.0 into a dual-stack
// socket that also accepts IPv6.
func obsSocket(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := checkBindHost(host); err != nil {
		return nil, err
	}
	network := "tcp"
	if net.ParseIP(host).To4() != nil {
		network = "tcp4"
	}
	return net.Listen(network, addr)
}

// ServeObs serves the page on addr (an IP address or localhost, with a port) until ctx ends.
func (r *Runner) ServeObs(ctx context.Context, addr string) error {
	h, err := r.obsHandler(CheckLoopback(addr) != nil)
	if err != nil {
		return err
	}
	return serveObs(ctx, addr, h)
}

// serveObs serves h on addr until ctx ends.
func serveObs(ctx context.Context, addr string, h http.Handler) error {
	l, err := obsSocket(addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loopbackHost(h string) bool {
	if x, _, err := net.SplitHostPort(h); err == nil {
		h = x
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ipHost reports whether a Host header names an IP literal, with or without a port.
func ipHost(h string) bool {
	if x, _, err := net.SplitHostPort(h); err == nil {
		h = x
	}
	return net.ParseIP(strings.Trim(h, "[]")) != nil
}

type crumb struct{ Label, Href string }

type sourceLine struct {
	ObsSource
	Last string
	// the gate proofs the machine ran in the page's window, and their wall time (gate.proof_run)
	Proofs   int     `json:"proof_runs,omitempty"`
	ProofMin float64 `json:"proof_minutes,omitempty"`
}

type obsPage struct {
	Kind      string
	Section   string // sidebar entry: now, spend, flow, machines
	Theme     string // light, dark or "" (follow the system)
	PFilter   string // header project filter ("" = all)
	Projects  []string
	Sel       url.Values // the request query, for links that keep the page's selection
	NowPg     *NowPage
	SpendPg   *SpendPage
	FlowPg    *FlowPage
	MachPg    *MachinesPage
	HubPg     *HubPage
	HubOnly   bool   // a hub with no factory behind it: no factory pages to link
	HasHub    bool   // this server has the All factories page
	HubURL    string // another server's All factories page (config obs_hub_url)
	Label     string // this factory's display name (config obs_label)
	Title     string
	Crumbs    []crumb
	F         ObsFilter
	Days      int
	Machines  []string
	Harnesses []string
	Now       string
	Me        string
	Portfolio []ProjectLine
	Sources   []sourceLine
	Project   string
	Goals     []GoalLine
	Goal      string
	GoalTitle string
	Tasks     []*ObsTask
	Task      *ObsTask
	TaskTitle string
	Sessions  []*ObsSession
	Session   *ObsSession
	Events    []Event
	AsksLocal bool
	NotFound  string
	obs       *Obs
	path      string
}

// Q is a link that keeps the filters: {{$.Q "/goal" "g" .Goal}}.
func (p *obsPage) Q(path string, kv ...string) string {
	v := url.Values{}
	if p.F.Machine != "" {
		v.Set("machine", p.F.Machine)
	}
	if p.F.Harness != "" {
		v.Set("harness", p.F.Harness)
	}
	if p.Days != 30 {
		v.Set("days", strconv.Itoa(p.Days))
	}
	if p.PFilter != "" {
		v.Set("project", p.PFilter)
	}
	if p.Theme != "" {
		v.Set("theme", p.Theme)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// ThemeQ is this page's own address with the theme switched, keeping the selection (p, g, k, s, by).
func (p *obsPage) ThemeQ(theme string) string {
	extra := []string{"theme", theme}
	for _, k := range []string{"p", "g", "k", "s", "by"} {
		if v := p.Sel.Get(k); v != "" {
			extra = append(extra, k, v)
		}
	}
	return p.Q(p.Path(), extra...)
}

// Ranges are the time ranges of the header (days), plus the current one.
func (p *obsPage) Ranges() []int {
	out := []int{7, 30, 90}
	for _, d := range out {
		if d == p.Days {
			return out
		}
	}
	out = append(out, p.Days)
	sort.Ints(out)
	return out
}

// Path is the page's own path (the filter form's action).
func (p *obsPage) Path() string { return p.path }

// SessionsOf is the task's sessions (template helper).
func (p *obsPage) SessionsOf(t *ObsTask) []*ObsSession { return p.obs.TaskSessions(t) }

var obsFuncs = template.FuncMap{
	"usd":         func(f float64) string { return fmt.Sprintf("$%.2f", f) },
	"dur":         dur,
	"spendGroups": func() []string { return SpendGroups },
	"fp":          fp,
	"short": func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	"tokens": func(n int64) string {
		switch {
		case n >= 1_000_000:
			return fmt.Sprintf("%.1fM", float64(n)/1e6)
		case n >= 1000:
			return fmt.Sprintf("%.0fk", float64(n)/1e3)
		}
		return strconv.FormatInt(n, 10)
	},
	"when": func(ts string) string {
		t := parseTS(ts)
		if t.IsZero() {
			return ts
		}
		return time.Unix(t.Unix(), 0).Format("2006-01-02 15:04") // local time
	},
	"summary": eventSummary,
	"traceID": TraceID,
	"outcomeClass": func(o string) string {
		switch {
		case o == "merged":
			return "ok"
		case strings.HasPrefix(o, "FAIL"), o == "reverted":
			return "bad"
		case strings.HasPrefix(o, "held"):
			return "warn"
		}
		return ""
	},
	"peerHref": peerHref,
	"taskHref": taskHref,
	"needClass": func(kind string) string {
		switch kind {
		case "question", "held":
			return "warn"
		case "failed":
			return "bad"
		}
		return ""
	},
	"ago": func(ts string) string {
		t := parseTS(ts)
		if t.IsZero() || time.Since(t) < 0 {
			return "0s"
		}
		return dur(int64(time.Since(t).Seconds()))
	},
	"safeURL": func(s string) template.URL {
		if strings.HasPrefix(s, "https://") {
			return template.URL(s)
		}
		return ""
	},
}

// eventSummary is the event's data as k=v pairs, short.
func eventSummary(e Event) string {
	ks := make([]string, 0, len(e.Data))
	for k := range e.Data {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var parts []string
	for _, k := range ks {
		var v string
		switch x := e.Data[k].(type) {
		case string:
			v = x
		default:
			b, _ := json.Marshal(x)
			v = string(b)
		}
		parts = append(parts, k+"="+truncate(v, 120))
	}
	if e.CostUSD != nil {
		parts = append([]string{fmt.Sprintf("cost=$%.2f", *e.CostUSD)}, parts...)
	}
	return truncate(strings.Join(parts, " "), 400)
}

// obsGuard sets the headers of every answer and refuses what obs never serves: a method that is not GET or HEAD, and
// a Host that is not loopback (or, bound wide, an IP literal). false: the answer is written.
func obsGuard(w http.ResponseWriter, req *http.Request, wide bool) bool {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	if req.URL.Path == "/api/obs/task" && req.Method != http.MethodGet {
		h.Set("Allow", "GET")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return false
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return false
	}
	if !loopbackHost(req.Host) && !(wide && ipHost(req.Host)) {
		http.Error(w, "host refused", http.StatusForbidden)
		return false
	}
	return true
}

func serveObsCSS(w http.ResponseWriter, req *http.Request) {
	b, err := obsFS.ReadFile("assets/obs/app.css")
	if err != nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	if req.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}

// renderObs writes one page of the shell.
func renderObs(w http.ResponseWriter, req *http.Request, t *template.Template, p *obsPage, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if req.Method == http.MethodHead {
		return
	}
	if err := t.Execute(w, p); err != nil {
		fmt.Fprintf(w, "<p>render: %s</p>", template.HTMLEscapeString(err.Error()))
	}
}

// redactSources drops what another machine has no use for: where the logs lie on disk and where they were pulled
// from. A pulled source keeps the word "pulled", so the page still tells it from a session machine.
func redactSources(srcs []ObsSource) {
	for i := range srcs {
		if srcs[i].Path != "" && !srcs[i].Own {
			srcs[i].Path = "pulled"
		} else {
			srcs[i].Path = ""
		}
		srcs[i].From = ""
	}
}

func (s *obsServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !obsGuard(w, req, s.wide) {
		return
	}
	if req.URL.Path == "/assets/obs/app.css" {
		serveObsCSS(w, req)
		return
	}
	jsonOut := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch req.URL.Path { // answers that need no page model
	case "/api/obs/v1/summary":
		sum, err := s.summary()
		if err != nil {
			http.Error(w, "obs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonOut(sum)
		return
	case "/api/obs/v1/factories":
		if s.hub == nil {
			break
		}
		sum, err := s.summary()
		if err != nil {
			http.Error(w, "obs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeFactories(w, s.hub.Factories(req.Context(), sum), s.now())
		return
	}
	q := req.URL.Query()
	f := ObsFilter{Machine: q.Get("machine"), Harness: q.Get("harness")}
	days, _ := strconv.Atoi(q.Get("days"))
	if days <= 0 || days > 3650 {
		days = 30
	}
	o, srcs, err := s.load(f)
	if err != nil {
		http.Error(w, "obs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.wide {
		redactSources(srcs)
	}
	p := &obsPage{F: f, Days: days, Machines: o.Machines, Harnesses: o.Harnesses, Now: o.Now.Format("2006-01-02 15:04 MST"),
		Me: s.r.Machine(), obs: o, path: req.URL.Path, Section: "now", PFilter: q.Get("project"), Sel: q,
		HasHub: s.hub != nil, Label: strings.TrimSpace(s.r.Store.Cfg("obs_label"))}
	if hu := strings.TrimSpace(s.r.Store.Cfg("obs_hub_url")); !p.HasHub && (strings.HasPrefix(hu, "http://") || strings.HasPrefix(hu, "https://")) {
		p.HubURL = strings.TrimRight(hu, "/") + "/all"
	}
	if th := q.Get("theme"); th == "light" || th == "dark" {
		p.Theme = th
	}
	pset := map[string]bool{}
	for _, t := range o.Tasks {
		pset[or(t.Project, NoProject)] = true
	}
	p.Projects = setKeys(pset)
	if p.PFilter != "" && !pset[p.PFilter] {
		p.Projects = append(p.Projects, p.PFilter)
	}
	p.Crumbs = []crumb{{"now", p.Q("/")}}
	code := http.StatusOK
	switch req.URL.Path {
	case "/all":
		if s.hub == nil {
			p.Kind, p.NotFound, code = "missing", "no page /all (set obs_peers to list other factories)", http.StatusNotFound
			break
		}
		sum, err := s.summary()
		if err != nil {
			http.Error(w, "obs: "+err.Error(), http.StatusInternalServerError)
			return
		}
		p.Kind, p.Section, p.Title = "hub", "hub", "All factories"
		p.HubPg = BuildHubPage(s.hub.Factories(req.Context(), sum))
	case "/spend", "/api/obs/spend":
		p.Kind, p.Section, p.Title = "spend", "spend", "Spend"
		p.SpendPg = o.BuildSpend(days, p.PFilter, q.Get("by"))
		p.SpendPg.Banners = p.SpendPg.pollingBanners(cfgInt(s.r.Store, "obs_banner_polling_pct"))
		if req.URL.Path == "/api/obs/spend" {
			jsonOut(map[string]any{"now": o.Now, "filter": f, "project": p.PFilter, "spend": p.SpendPg})
			return
		}
	case "/flow", "/api/obs/flow":
		p.Kind, p.Section, p.Title = "flow", "flow", "Flow"
		p.FlowPg = o.BuildFlow(days, p.PFilter)
		p.FlowPg.Banners = p.FlowPg.roundsBanners(cfgInt(s.r.Store, "obs_banner_rounds_pct"))
		p.FlowPg.Banners = append(p.FlowPg.Banners, p.FlowPg.stageBanners(cfgInt(s.r.Store, "obs_banner_stage_pct"))...)
		if req.URL.Path == "/api/obs/flow" {
			jsonOut(map[string]any{"now": o.Now, "filter": f, "project": p.PFilter, "flow": p.FlowPg})
			return
		}
	case "/machines", "/api/obs/machines":
		p.Kind, p.Section, p.Title = "machines", "machines", "Machines"
		p.MachPg = s.machinesPage(o)
		if req.URL.Path == "/api/obs/machines" {
			jsonOut(map[string]any{"now": o.Now, "cap": p.MachPg.Cap, "machines": p.MachPg.Cards})
			return
		}
	case "/", "/now", "/api/obs/now", "/api/portfolio":
		p.Kind, p.Title = "now", "Now"
		asks, ok := s.r.OpenAsks()
		p.AsksLocal = ok
		p.NowPg = o.BuildNow(days, p.PFilter, p.Me, o.OpenAsks(p.Me, asks, ok), ok, srcs)
		p.Portfolio = p.NowPg.Portfolio
		for _, src := range srcs {
			p.Sources = append(p.Sources, sourceLine{ObsSource: src, Last: o.LastSeen[src.Machine]})
		}
		if req.URL.Path == "/api/obs/now" {
			jsonOut(map[string]any{"now": o.Now, "filter": f, "project": p.PFilter, "days": days, "page": p.NowPg})
			return
		}
		if req.URL.Path == "/api/portfolio" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"now": o.Now, "filter": f, "days": days, "projects": p.Portfolio,
				"machines": o.Machines, "harnesses": o.Harnesses, "sources": p.Sources, "open_asks_local_only": true})
			return
		}
	case "/project":
		p.Kind, p.Project = "project", q.Get("p")
		p.Title = "Project " + p.Project
		p.Crumbs = append(p.Crumbs, crumb{p.Project, ""})
		for _, g := range o.Goals(projectKey(p.Project)) {
			if g.origins[p.Me] { // goal ids are per factory DB: only a goal this machine ran has its title here
				g.Title = s.goalTitle(g.Goal)
			}
			p.Goals = append(p.Goals, g)
		}
	case "/goal":
		p.Kind, p.Goal = "goal", q.Get("g")
		p.Tasks = o.TaskList("", p.Goal)
		if pr := q.Get("p"); pr != "" {
			p.Tasks = o.TaskList(projectKey(pr), p.Goal)
			p.Project = pr
		} else if len(p.Tasks) > 0 {
			p.Project = or(p.Tasks[0].Project, NoProject)
		}
		for _, t := range p.Tasks {
			if t.Origin == p.Me {
				p.GoalTitle = s.goalTitle(p.Goal)
				break
			}
		}
		p.Title = "Goal " + or(p.Goal, "(none)")
		p.Crumbs = append(p.Crumbs, crumb{p.Project, p.Q("/project", "p", p.Project)}, crumb{or(p.Goal, "(no goal)"), ""})
	case "/task":
		p.Kind = "task"
		p.Task = o.Tasks[q.Get("k")]
		if p.Task == nil {
			p.NotFound, code = "no task "+q.Get("k")+" (with these filters)", http.StatusNotFound
			break
		}
		t := p.Task
		p.Title = "Task " + t.Task
		if t.Origin == s.r.Machine() {
			if x, err := s.r.Store.Task(t.Task); err == nil && x != nil {
				p.TaskTitle = x.Title
			}
		}
		p.Sessions, p.Events = o.TaskSessions(t), t.Events
		p.Crumbs = append(p.Crumbs, crumb{or(t.Project, NoProject), p.Q("/project", "p", or(t.Project, NoProject))},
			crumb{or(t.Goal, "(no goal)"), p.Q("/goal", "g", t.Goal, "p", or(t.Project, NoProject))}, crumb{t.Task + " @" + t.Machine, ""})
	case "/api/obs/task":
		t := o.Tasks[q.Get("k")]
		if t == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no task " + q.Get("k") + " (with these filters)"})
			return
		}
		jsonOut(buildTrace(t))
		return
	case "/session":
		p.Kind = "session"
		p.Session = o.Sessions[q.Get("s")]
		if p.Session == nil {
			p.NotFound, code = "no session "+q.Get("s")+" (with these filters)", http.StatusNotFound
			break
		}
		x := p.Session
		p.Title, p.Events = "Session "+x.ID, x.Events
		if t := o.Tasks[x.TaskKey]; t != nil {
			p.Task = t
			p.Crumbs = append(p.Crumbs, crumb{or(t.Project, NoProject), p.Q("/project", "p", or(t.Project, NoProject))},
				crumb{or(t.Goal, "(no goal)"), p.Q("/goal", "g", t.Goal, "p", or(t.Project, NoProject))},
				crumb{t.Task + " @" + t.Machine, p.Q("/task", "k", t.Key)})
		}
		p.Crumbs = append(p.Crumbs, crumb{"session " + x.ID, ""})
	default:
		p.Kind, p.NotFound, code = "missing", "no page "+req.URL.Path, http.StatusNotFound
	}
	renderObs(w, req, s.tmpl, p, code)
}

// projectKey maps the page's label for tasks without a project back to "".
func projectKey(p string) string {
	if p == NoProject {
		return ""
	}
	return p
}

// OpenAsks counts this machine's open owner questions per project from its DB (pulled machines: Obs.OpenAsks).
func (r *Runner) OpenAsks() (map[string]int, bool) {
	sq, err := r.sq()
	if err != nil {
		return nil, false
	}
	asks, err := sq.Asks("open")
	if err != nil {
		return nil, false
	}
	out := map[string]int{}
	for _, a := range asks {
		out[a.Project]++
	}
	return out, true
}

func (s *obsServer) goalTitle(id string) string {
	if id == "" {
		return ""
	}
	sq, err := s.r.sq()
	if err != nil {
		return ""
	}
	if g, err := sq.Goal(id); err == nil && g != nil {
		return g.Title
	}
	return ""
}
