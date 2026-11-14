package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CI conclusions that count as failing or passing (web/ci.py FAIL and OK).
var (
	ciFail = map[string]bool{"failure": true, "failed": true, "error": true, "timed_out": true, "cancelled": true, "action_required": true, "startup_failure": true}
	ciOK   = map[string]bool{"success": true, "neutral": true, "skipped": true}
)

// Check is one CI check of a pull request.
type Check struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// CIEntry is a cache entry: the PR state ("unknown" on any gh failure), its checks and the fetch time.
type CIEntry struct {
	State   string  `json:"state"`
	Checks  []Check `json:"checks"`
	Fetched float64 `json:"fetched"`
}

// CIChecks is ci._checks: the name and lowercased conclusion of each statusCheckRollup item (non-objects skipped).
func CIChecks(rollup any) []Check {
	out := []Check{}
	list, _ := rollup.([]any)
	for _, it := range list {
		c, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := firstTruthy(c["name"], c["context"])
		if name == "" {
			name = "?"
		}
		concl := strings.ToLower(firstTruthy(c["conclusion"], c["state"], c["status"]))
		if concl == "" {
			concl = "pending"
		}
		out = append(out, Check{trunc80(name), concl})
	}
	return out
}

func firstTruthy(vs ...any) string {
	for _, v := range vs {
		if truthy(v) {
			return pyStr(v)
		}
	}
	return ""
}

func trunc80(s string) string {
	r := []rune(s)
	if len(r) > 80 {
		r = r[:80]
	}
	return string(r)
}

// CISummarize is ci.summarize: green | failing | pending | unknown for a cache entry (nil = unknown).
func CISummarize(e *CIEntry) string {
	if e == nil || e.State == "unknown" {
		return "unknown"
	}
	pending := len(e.Checks) == 0
	for _, c := range e.Checks {
		if ciFail[c.Conclusion] {
			return "failing"
		}
		if !ciOK[c.Conclusion] {
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "green"
}

// GhRunner runs `gh <args>` and returns its stdout; tests replace it at the process boundary.
type GhRunner func(ctx context.Context, gh string, args ...string) ([]byte, error)

func execGh(ctx context.Context, gh string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gh, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CICache is ci.CiCache: cached CI status for open PRs. Get never blocks and never calls gh itself: a miss (or an
// expired entry) queues the PR for one background worker that runs `gh pr view <url> --json state,statusCheckRollup`
// (no shell, timeout) for at most one PR at a time. Any gh failure is cached as state "unknown". At most MaxPRs PRs
// are tracked at once.
type CICache struct {
	Gh        string
	TTL       float64
	Timeout   time.Duration
	MaxPRs    int
	Clock     func() float64
	Run       GhRunner
	Autostart bool

	mu      sync.Mutex
	cv      *sync.Cond
	entries map[string]CIEntry
	seen    map[string]float64
	queue   []string
	queued  map[string]bool
	running bool
}

// NewCICache returns a cache with the Python defaults (gh, ttl 300 s, timeout 10 s, 20 PRs, autostart).
func NewCICache() *CICache {
	c := &CICache{Gh: "gh", TTL: 300, Timeout: 10 * time.Second, MaxPRs: 20, Autostart: true, Run: execGh,
		Clock:   func() float64 { return tsec(time.Now()) },
		entries: map[string]CIEntry{}, seen: map[string]float64{}, queued: map[string]bool{}}
	c.cv = sync.NewCond(&c.mu)
	return c
}

// Get returns the cached entry (possibly expired, never blocking) or nil, and queues a refresh when it is stale.
func (c *CICache) Get(prURL string) *CIEntry {
	if prURL == "" {
		return nil
	}
	now := c.Clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	for u, t := range c.seen {
		if now-t > 2*c.TTL && !c.queued[u] {
			delete(c.seen, u)
			delete(c.entries, u)
		}
	}
	if _, ok := c.seen[prURL]; !ok && len(c.seen) >= c.MaxPRs {
		return nil
	}
	c.seen[prURL] = now
	e, have := c.entries[prURL]
	if (!have || now-e.Fetched >= c.TTL) && !c.queued[prURL] {
		c.queued[prURL] = true
		c.queue = append(c.queue, prURL)
		c.cv.Broadcast()
		if c.Autostart && !c.running {
			c.running = true
			go c.RunWorker()
		}
	}
	if !have {
		return nil
	}
	cp := e
	return &cp
}

// Fetch is one blocking gh call; it returns a cache entry and every failure becomes state "unknown".
func (c *CICache) Fetch(prURL string) CIEntry {
	entry := CIEntry{State: "unknown", Checks: []Check{}, Fetched: c.Clock()}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	out, err := c.Run(ctx, c.Gh, "pr", "view", prURL, "--json", "state,statusCheckRollup")
	if err != nil {
		return entry
	}
	var j map[string]any
	if json.Unmarshal(out, &j) != nil {
		return entry
	}
	if truthy(j["state"]) {
		entry.State = strings.ToLower(pyStr(j["state"]))
	}
	entry.Checks = CIChecks(j["statusCheckRollup"])
	return entry
}

// Step processes one queued PR and reports whether one was handled. Only the worker (or a test) calls it.
// wait is how long to wait for work when the queue is empty (0 = do not wait).
func (c *CICache) Step(wait time.Duration) bool {
	c.mu.Lock()
	if len(c.queue) == 0 && wait > 0 {
		timer := time.AfterFunc(wait, func() { c.mu.Lock(); c.cv.Broadcast(); c.mu.Unlock() })
		c.cv.Wait()
		timer.Stop()
	}
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return false
	}
	url := c.queue[0]
	c.queue = c.queue[1:]
	c.mu.Unlock()
	entry := c.Fetch(url)
	c.mu.Lock()
	c.entries[url] = entry
	delete(c.queued, url)
	c.mu.Unlock()
	return true
}

// RunWorker loops Step(60 s) forever; it is the one background worker.
func (c *CICache) RunWorker() {
	for {
		c.Step(60 * time.Second)
	}
}
