package hm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// The hub: one page and one JSON answer over several factories. It asks each peer's /api/obs/v1/summary, keeps the last
// good answer of each, and links into the peers' own pages. It has no database and no event log. A factory whose config
// lists obs_peers serves it at /all, itself first; `hivemill obs --hub --peers ...` serves it on a machine with no
// factory. Peer URLs come only from local config, never from a request. Design: docs/design/multi-factory.md.

const (
	hubTimeout  = 3 * time.Second  // one peer's answer
	hubTTL      = 15 * time.Second // a peer is asked at most this often
	hubStale    = 60 * time.Second // an answer older than this is marked stale
	hubMaxBytes = 2 << 20
)

// HubPeer is one factory as the hub sees it: its last good summary, and how the last ask went.
type HubPeer struct {
	URL       string   `json:"url"` // "" for the factory that serves the hub
	Self      bool     `json:"self,omitempty"`
	FetchedAt string   `json:"fetched_at,omitempty"` // when the summary was fetched
	AgeSec    int64    `json:"age_sec"`
	Stale     bool     `json:"stale"`
	Error     string   `json:"error,omitempty"`
	Summary   *Summary `json:"summary"` // null: never answered
}

type hubPeerState struct {
	mu      sync.Mutex // one ask at a time
	sum     *Summary
	okAt    time.Time
	triedAt time.Time
	err     string
}

// Hub asks the peers and caches their answers.
type Hub struct {
	Peers   []string
	client  *http.Client
	timeout time.Duration
	now     func() time.Time
	mu      sync.Mutex
	state   map[string]*hubPeerState
}

// ParsePeers reads obs_peers: comma-separated http(s) URLs of other factories' obs servers, each a bare origin.
func ParsePeers(cfg string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range csv(cfg) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("obs peer %q: use http://HOST:PORT", raw)
		}
		p := u.Scheme + "://" + u.Host
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// NewHub is a hub over peers.
func NewHub(peers []string) *Hub {
	return &Hub{Peers: peers, now: time.Now, state: map[string]*hubPeerState{}, timeout: hubTimeout,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (h *Hub) peer(u string) *hubPeerState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state[u]
	if st == nil {
		st = &hubPeerState{}
		h.state[u] = st
	}
	return st
}

// ask fetches one peer's summary, at most once per hubTTL. A failure keeps the last good answer.
func (h *Hub) ask(ctx context.Context, u string) HubPeer {
	st := h.peer(u)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.triedAt.IsZero() || h.now().Sub(st.triedAt) >= hubTTL {
		sum, err := h.fetch(ctx, u)
		st.triedAt = h.now()
		if err != nil {
			st.err = err.Error()
		} else {
			st.sum, st.okAt, st.err = sum, st.triedAt, ""
		}
	}
	p := HubPeer{URL: u, Error: st.err, Summary: st.sum, Stale: st.err != ""}
	if !st.okAt.IsZero() {
		p.FetchedAt = st.okAt.UTC().Format(time.RFC3339)
		p.AgeSec = int64(h.now().Sub(st.okAt).Seconds())
		p.Stale = p.Stale || h.now().Sub(st.okAt) > hubStale
	}
	return p
}

// fetch errors are short and say nothing a page should not show.
func (h *Hub) fetch(ctx context.Context, u string) (*Summary, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"/api/obs/v1/summary", nil)
	if err != nil {
		return nil, errors.New("bad address")
	}
	res, err := h.client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return nil, errors.New("no answer in time")
		}
		return nil, errors.New("unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("no API v1: update this factory")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var sum Summary
	if err := json.NewDecoder(io.LimitReader(res.Body, hubMaxBytes)).Decode(&sum); err != nil || sum.API < 1 {
		return nil, errors.New("not a summary")
	}
	if sum.NeedsYou == nil {
		sum.NeedsYou = []Need{}
	}
	return &sum, nil
}

// Factories asks every peer at once. self, when given, is the serving factory's own summary and comes first.
func (h *Hub) Factories(ctx context.Context, self *Summary) []HubPeer {
	out := make([]HubPeer, len(h.Peers))
	var wg sync.WaitGroup
	for i, u := range h.Peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = h.ask(ctx, u)
		}()
	}
	wg.Wait()
	if self != nil {
		out = append([]HubPeer{{Self: true, Summary: self, FetchedAt: self.Now.UTC().Format(time.RFC3339)}}, out...)
	}
	return out
}

// ---- the page ---------------------------------------------------------------------------------------------------

// HubRow is one factory's row of the page.
type HubRow struct {
	HubPeer
	Label string
	State string // "" (fresh) | stale | unreachable | never
	Note  string // the state in words, with its age
	Down  int    // machines down
}

// HubNeed is one row of the page's Needs you, with the factory it came from.
type HubNeed struct {
	Need
	Factory string
	Base    string
}

// HubPage is the model of /all.
type HubPage struct {
	Rows       []HubRow
	Needs      []HubNeed
	NeedsCount int // act rows over all fresh factories
}

// BuildHubPage lays the peers side by side. Rows of a stale or unreachable factory keep their last numbers; its needs
// are not listed, since nobody can tell whether they still hold.
func BuildHubPage(peers []HubPeer) *HubPage {
	p := &HubPage{}
	for i, f := range peers {
		row := HubRow{HubPeer: f, Label: fmt.Sprintf("Factory %d", i+1)}
		if f.Summary != nil && f.Summary.Runner.Label != "" {
			row.Label = f.Summary.Runner.Label
		}
		switch {
		case f.Summary == nil && f.Error != "":
			row.State, row.Note = "never", "has not answered: "+f.Error
		case f.Summary == nil:
			row.State, row.Note = "never", "has not answered"
		case f.Error != "":
			row.State, row.Note = "unreachable", fmt.Sprintf("%s; last answer %s ago", f.Error, dur(f.AgeSec))
		case f.Stale:
			row.State, row.Note = "stale", "stale, "+dur(f.AgeSec)
		}
		if f.Summary != nil {
			for _, m := range f.Summary.Machines {
				if m.Down {
					row.Down++
				}
			}
			if row.State == "" {
				for _, n := range f.Summary.NeedsYou {
					p.Needs = append(p.Needs, HubNeed{Need: n, Factory: row.Label, Base: f.URL})
				}
				p.NeedsCount += f.Summary.NeedsCount
			}
		}
		if row.State == "unreachable" || row.State == "never" {
			p.Needs = append(p.Needs, HubNeed{Need: Need{Kind: "system", Text: row.Note, AgeSec: f.AgeSec, Href: "/"}, Factory: row.Label, Base: f.URL})
		}
		p.Rows = append(p.Rows, row)
	}
	sort.SliceStable(p.Needs, func(i, j int) bool {
		if a, b := needOrder[p.Needs[i].Kind], needOrder[p.Needs[j].Kind]; a != b {
			return a < b
		}
		return p.Needs[i].AgeSec > p.Needs[j].AgeSec
	})
	return p
}

// peerHref is a link into a factory's own pages: base is a peer URL from local config ("" = this server), href a path
// on it or an https URL (a PR).
func peerHref(base, href string) template.URL {
	if strings.HasPrefix(href, "https://") {
		return template.URL(href)
	}
	if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
		href = "/"
	}
	if base == "" {
		return template.URL(href)
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return template.URL(strings.TrimRight(base, "/") + href)
}

// ---- a hub with no factory behind it ---------------------------------------------------------------------------------

type hubServer struct {
	hub  *Hub
	tmpl *template.Template
	wide bool
}

// HubHandler serves only the hub: /, /all, /api/obs/v1/factories and the stylesheet.
func HubHandler(hub *Hub, wide bool) (http.Handler, error) {
	t, err := template.New("page.html").Funcs(obsFuncs).ParseFS(obsFS, "assets/obs/*.html")
	if err != nil {
		return nil, err
	}
	return &hubServer{hub: hub, tmpl: t, wide: wide}, nil
}

func (s *hubServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !obsGuard(w, req, s.wide) {
		return
	}
	if req.URL.Path == "/assets/obs/app.css" {
		serveObsCSS(w, req)
		return
	}
	switch req.URL.Path {
	case "/api/obs/v1/factories":
		writeFactories(w, s.hub.Factories(req.Context(), nil), s.hub.now())
	case "/", "/all":
		p := &obsPage{Kind: "hub", Section: "hub", Title: "All factories", HubOnly: true, HasHub: true, Days: 30, path: "/all", Sel: req.URL.Query(),
			Now: s.hub.now().Format("2006-01-02 15:04 MST"), HubPg: BuildHubPage(s.hub.Factories(req.Context(), nil))}
		if th := req.URL.Query().Get("theme"); th == "light" || th == "dark" {
			p.Theme = th
		}
		renderObs(w, req, s.tmpl, p, http.StatusOK)
	default:
		p := &obsPage{Kind: "missing", HubOnly: true, HasHub: true, Days: 30, path: req.URL.Path, Sel: url.Values{}, NotFound: "no page " + req.URL.Path,
			Now: s.hub.now().Format("2006-01-02 15:04 MST")}
		renderObs(w, req, s.tmpl, p, http.StatusNotFound)
	}
}

func writeFactories(w http.ResponseWriter, peers []HubPeer, now time.Time) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"api": ObsAPI, "now": now, "factories": peers})
}

// ServeHub serves a hub with no factory behind it on addr until ctx ends.
func ServeHub(ctx context.Context, addr string, peers []string) error {
	h, err := HubHandler(NewHub(peers), CheckLoopback(addr) != nil)
	if err != nil {
		return err
	}
	return serveObs(ctx, addr, h)
}
