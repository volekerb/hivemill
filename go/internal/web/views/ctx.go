package views

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Machine is resources.machine as the Overview reads it: the 1-minute load average (nil = unknown) and the CPU count.
type Machine struct {
	Load1 *float64
	CPUs  int
}

// Ctx is what a view gets: a per-request read-only connection plus injected helpers (web/server.py Ctx). A nil
// function is "not available", like None in Python.
type Ctx struct {
	DB       *data.Conn
	Now      time.Time
	Query    map[string]string // str -> first value of the query string; views validate what they use
	Defaults []data.Default
	// Tokens returns the TokenIndex snapshot; nil or a not-ready snapshot shows "not ready", never a zero.
	Tokens func() *tokens.Snapshot
	// Usage is usage_fn (the Claude usage status line); Mem is mem_fn (MB available); Machine is resources.machine.
	Usage   func() *data.Usage
	Mem     func() *float64
	Machine func() Machine
	// CI is ci.get: the cached CI state of a PR (never blocks); nil or a nil result reads as "unknown".
	CI func(prURL string) *data.CIEntry
	// Cursor is cursor_limits.display(db, now). Nil falls back to the stored limit state with 0 agent-hours (the
	// agent-hours come from web.views.tokens.cursor_activity, ported with the tokens view).
	Cursor func() (*tokens.DisplayData, error)
}

// View is one page: Render reads the data it needs through ctx and returns the page title and body.
type View interface {
	Render(ctx *Ctx, params map[string]string) (title string, body Safe, err error)
}

func (c *Ctx) org() string { return data.OrgParam(c.Query) }

func (c *Ctx) cursor() (*tokens.DisplayData, error) {
	if c.Cursor != nil {
		return c.Cursor()
	}
	d, err := tokens.Display(c.DB.DB, 0)
	if err != nil {
		return nil, fmt.Errorf("views: cursor limits: %w", err)
	}
	return d, nil
}

// cfgNum is queries._cfg_num: the config value, else the default, else the fallback (also when it does not parse).
func (c *Ctx) cfgNum(key string, fallback float64) (float64, error) {
	rows, err := c.DB.Config()
	if err != nil {
		return 0, fmt.Errorf("views: read config: %w", err)
	}
	var raw string
	found := false
	for _, r := range rows {
		if r.Key == key {
			raw, found = r.Value, true
		}
	}
	if !found {
		for _, d := range c.Defaults {
			if d.Key == key {
				return d.Value.V, nil
			}
		}
		return fallback, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback, nil
	}
	return f, nil
}

var (
	// reS is Python's \s (str.isspace): ASCII whitespace, the FS-US separators, NEL, NBSP and the Unicode Z* spaces.
	reS     = `[\t-\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`
	wsRe    = regexp.MustCompile(reS + `+`)
	scTagRe = regexp.MustCompile(`\[(SC\p{Nd}+(?:` + reS + `*,` + reS + `*SC\p{Nd}+)*)\]`)
	orgPre  = regexp.MustCompile(`^(claude|cursor)-`)
)

// stripTags is queries._strip_tags: [SCn] criterion tags removed, whitespace collapsed.
func stripTags(title string) string {
	return pyStrip(wsRe.ReplaceAllString(scTagRe.ReplaceAllString(title, " "), " "))
}

// trunc is queries._t: at most n characters, the last one an ellipsis when cut.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// orgPrefix is scout.orgs.prefix_of: "cursor" for "cursor-coder-t-1", "" for an unprefixed name.
func orgPrefix(name string) string {
	if m := orgPre.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
