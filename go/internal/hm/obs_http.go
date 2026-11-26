package hm

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// `hivemill obs`: a read-only dashboard on 127.0.0.1. html/template pages and one embedded stylesheet, no script,
// nothing loaded from another origin. Sidebar: Now, Spend, Flow; the drill-down zoom (project -> goal -> task ->
// session) stays under Now. Header: project, time range, machine, harness, theme. JSON of every page: /api/obs/*.

//go:embed assets/obs
var obsFS embed.FS

// ObsAddr is the listen address: loopback only.
func ObsAddr(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

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
	s.r.listMachines(o, sources)
	for i := range srcs {
		srcs[i].Machine = sources[i]
	}
	return o, srcs, nil
}

// ObsHandler is the page. It answers GET and HEAD only, and only for a loopback Host (no DNS rebinding).
func (r *Runner) ObsHandler() (http.Handler, error) {
	t, err := template.New("page.html").Funcs(obsFuncs).ParseFS(obsFS, "assets/obs/*.html")
	if err != nil {
		return nil, err
	}
	s := &obsServer{r: r, tmpl: t, now: time.Now}
	return s, nil
}

// ServeObs serves the page on addr until ctx ends.
func (r *Runner) ServeObs(ctx context.Context, addr string) error {
	if err := CheckLoopback(addr); err != nil {
		return err
	}
	h, err := r.ObsHandler()
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
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	Section   string // sidebar entry: now, spend, flow
	Theme     string // light, dark or "" (follow the system)
	PFilter   string // header project filter ("" = all)
	Projects  []string
	Sel       url.Values // the request query, for links that keep the page's selection
	NowPg     *NowPage
	SpendPg   *SpendPage
	FlowPg    *FlowPage
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

func (s *obsServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return
	}
	if !loopbackHost(req.Host) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if req.URL.Path == "/assets/obs/app.css" {
		b, err := obsFS.ReadFile("assets/obs/app.css")
		if err != nil {
			http.NotFound(w, req)
			return
		}
		h.Set("Content-Type", "text/css; charset=utf-8")
		if req.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
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
	p := &obsPage{F: f, Days: days, Machines: o.Machines, Harnesses: o.Harnesses, Now: o.Now.Format("2006-01-02 15:04 MST"),
		Me: s.r.Machine(), obs: o, path: req.URL.Path, Section: "now", PFilter: q.Get("project"), Sel: q}
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
	jsonOut := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch req.URL.Path {
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
	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if req.Method == http.MethodHead {
		return
	}
	if err := s.tmpl.Execute(w, p); err != nil {
		fmt.Fprintf(w, "<p>render: %s</p>", template.HTMLEscapeString(err.Error()))
	}
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
