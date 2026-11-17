package views

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// NavItem is one nav entry: path, label, icon key.
type NavItem struct{ Path, Label, Icon string }

// NavGroup is a labelled group of NavItems.
type NavGroup struct {
	Label string
	Items []NavItem
}

// Groups is html.GROUPS (nav groups); Tabs is html.TABS (the phone tab bar).
var (
	Groups = []NavGroup{
		{"Work", []NavItem{{"/", "Overview", "grid"}, {"/goals", "Goals", "target"}, {"/tasks", "Tasks", "list"}, {"/agents", "Agents", "user"}}},
		{"Insights", []NavItem{{"/throughput", "Throughput", "bars"}, {"/quality", "Quality", "check"}, {"/tokens", "Tokens", "db"}, {"/changelog", "Changelog", "lines"}}},
		{"System", []NavItem{{"/resources", "Resources", "chip"}}},
	}
	Tabs = []string{"/", "/goals", "/tasks", "/agents"}
)

// Labels is html.LABELS: one label map for every enum shown to people.
var Labels = map[string]string{"todo": "To do", "in_progress": "In progress", "in_review": "In review", "blocked": "Blocked", "done": "Done",
	"cancelled": "Cancelled", "proposed": "Proposed", "planning": "Planning", "active": "Active", "live": "Running",
	"queued": "Queued", "exited": "Ended", "stopped": "Ended", "lost": "Lost", "parked": "Parked"}

var svg16 = map[string]string{
	"grid": `<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/>` +
		`<rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/>`,
	"target": `<circle cx="8" cy="8" r="6"/><circle cx="8" cy="8" r="2.5"/>`,
	"list":   `<path d="M3 4.5 4.5 6 7 3.5M3 10.5 4.5 12 7 9.5M9.5 5h4M9.5 11h4"/>`,
	"user":   `<circle cx="8" cy="5.5" r="2.5"/><path d="M3 13.5c.5-3 2.5-4 5-4s4.5 1 5 4"/>`,
	"bars":   `<path d="M3 13V8M8 13V3M13 13V6"/>`,
	"check":  `<circle cx="8" cy="8" r="6"/><path d="M5.2 8.2l2 2 3.6-4"/>`,
	"db":     `<ellipse cx="8" cy="4.5" rx="5" ry="2"/><path d="M3 4.5v7c0 1.1 2.2 2 5 2s5-.9 5-2v-7M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"/>`,
	"lines":  `<path d="M3 4h10M3 8h10M3 12h6"/>`,
	"chip":   `<rect x="3" y="3" width="10" height="10" rx="2"/><path d="M6 1.5V3M10 1.5V3M6 13v1.5M10 13v1.5"/>`,
	"more":   `<circle cx="3.5" cy="8" r="1"/><circle cx="8" cy="8" r="1"/><circle cx="12.5" cy="8" r="1"/>`,
}

const ring = `<circle cx="6" cy="6" r="4.25" fill="none" stroke="currentColor" stroke-width="1.5"/>`

type glyphDef struct{ tone, inner string }

// svg12 are the status glyphs: shape alone tells the states apart.
var svg12 = func() map[string]glyphDef {
	m := map[string]glyphDef{
		"todo":        {"neutral", ring},
		"cancelled":   {"neutral", ring + `<path d="M3.2 8.8 8.8 3.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>`},
		"proposed":    {"neutral", `<circle cx="6" cy="6" r="4.25" fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="2.2 1.6"/>`},
		"planning":    {"info", ring + `<path d="M6 6V2.9A3.1 3.1 0 0 1 9.1 6Z" fill="currentColor"/>`},
		"in_progress": {"info", ring + `<path d="M6 2.9A3.1 3.1 0 0 1 6 9.1Z" fill="currentColor"/>`},
		"in_review":   {"review", ring + `<path d="M6 6V2.9A3.1 3.1 0 1 1 2.9 6Z" fill="currentColor"/><circle cx="6" cy="6" r="1" fill="var(--surface-1)"/>`},
		"blocked":     {"danger", `<path d="M4 1.2h4L10.8 4v4L8 10.8H4L1.2 8V4Z" fill="currentColor"/><path d="M3.8 6h4.4" stroke="var(--surface-1)" stroke-width="1.5" fill="none"/>`},
		"done":        {"success", `<circle cx="6" cy="6" r="5.25" fill="currentColor"/><path d="M3.7 6.2 5.2 7.7 8.4 4.4" stroke="var(--surface-1)" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/>`},
		"live":        {"success", `<circle cx="6" cy="6" r="3.2" fill="currentColor"/>`},
		"queued":      {"warning", ring + `<path d="M6 3.6V6l1.7 1" stroke="currentColor" stroke-width="1.3" fill="none" stroke-linecap="round"/>`},
		"lost":        {"danger", `<path d="M6 1.4 11 10.4H1Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M6 4.6v2.6M6 8.6v.1" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>`},
		"exited":      {"neutral", `<circle cx="6" cy="6" r="3.2" fill="none" stroke="currentColor" stroke-width="1.5"/>`},
		"parked":      {"neutral", `<path d="M4.2 2.5v7M7.8 2.5v7" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`},
	}
	m["active"] = m["in_progress"]
	m["stopped"] = m["exited"]
	return m
}()

var healthGlyph = map[string]string{"success": "done", "warning": "queued", "danger": "lost", "neutral": "exited"}

func glyphOf(key string) glyphDef {
	if g, ok := svg12[key]; ok {
		return g
	}
	return svg12["todo"]
}

// pyCapitalize is str.capitalize: first rune upper, the rest lower.
func pyCapitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	for i := range r {
		if i == 0 {
			r[i] = unicode.ToUpper(r[i])
		} else {
			r[i] = unicode.ToLower(r[i])
		}
	}
	return string(r)
}

// LabelOf is LABELS.get(key, key.replace("_", " ").capitalize()).
func LabelOf(key string) string {
	if l, ok := Labels[key]; ok {
		return l
	}
	return pyCapitalize(strings.ReplaceAll(key, "_", " "))
}

func svg16Of(inner string, size int) Safe {
	return Safe(fmt.Sprintf(`<svg class="g" viewBox="0 0 16 16" width="%d" height="%d" fill="none" stroke="currentColor" `+
		`stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg>`, size, size, inner))
}

// Glyph is the 12 px status glyph (aria-hidden) for an enum key, tinted with the tone's solid colour.
func Glyph(key string) Safe {
	g := glyphOf(key)
	return Safe(`<svg class="g gs-` + g.tone + `" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">` + g.inner + `</svg>`)
}

// Status is the shape glyph plus the human label of an enum; pill adds the tinted background.
func Status(key string, pill bool) Safe {
	tone := glyphOf(key).tone
	cls := "st fg-" + tone
	if pill {
		cls = "st st--pill tone-" + tone
	}
	return El("span", Glyph(key), LabelOf(key), Cls(cls))
}

// Crumb is one breadcrumb item; the last one is the current page.
type Crumb struct{ Href, Text string }

// Crumbs is html.crumbs.
func Crumbs(items ...Crumb) Safe {
	var out []any
	for n, c := range items {
		if n > 0 {
			out = append(out, El("span", "/", Cls("sep"), At("aria-hidden", "true")))
		}
		if n < len(items)-1 {
			out = append(out, El("a", At("href", c.Href), c.Text))
		} else {
			out = append(out, El("span", At("aria-current", "location"), c.Text))
		}
	}
	return El("nav", out, At("aria-label", "Breadcrumb"), Cls("crumbs"))
}

func isOrg(o string) bool {
	for _, n := range data.Orgs {
		if n == o {
			return true
		}
	}
	return false
}

// OrgLabel is scout.orgs.label.
func OrgLabel(org string) string { return pyCapitalize(org) }

func withOrg(path, org string) string {
	if isOrg(org) {
		return path + "?org=" + org
	}
	return path
}

func wordmark(org string) Safe {
	return El("a", Safe(`<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.5" `+
		`stroke-linejoin="round" aria-hidden="true"><path d="M8 1.5 13.8 4.75v6.5L8 14.5 2.2 11.25v-6.5Z"/>`+
		`<path d="M8 5.2v5.6M5.1 6.9 8 8.5l2.9-1.6" stroke-width="1.2"/></svg>`), El("span", "Hivemill"),
		At("href", withOrg("/", org)), Cls("wordmark"))
}

// ShellView is queries.shell_status plus the page's "now".
type ShellView struct {
	data.Shell
	Now time.Time
}

func (s *ShellView) badges() map[string]struct {
	n    int64
	aria string
	bad  bool
} {
	var needs, attn int64
	if s != nil {
		needs, attn = int64(s.Needs), s.AgentsAttention
	}
	return map[string]struct {
		n    int64
		aria string
		bad  bool
	}{
		"/":       {needs, strconv.FormatInt(needs, 10) + " items need you", true},
		"/agents": {attn, strconv.FormatInt(attn, 10) + " agents queued or blocked", false},
	}
}

func navCount(path string, s *ShellView) any {
	b, ok := s.badges()[path]
	if !ok || b.n == 0 {
		return nil
	}
	cls := "count"
	if b.bad {
		cls = "count count--bad"
	}
	return El("span", b.n, Cls(cls), At("aria-label", b.aria))
}

// OrgDot is the small round swatch in the org's colour.
func OrgDot(org string) Safe {
	cls := "org-dot"
	if isOrg(org) {
		cls = "org-dot org-dot-" + org
	}
	return El("span", Cls(cls), At("aria-hidden", "true"))
}

// OrgFilter is the global org filter: segmented "All · Claude · Cursor" links to path with ?org= set.
func OrgFilter(path, current string) Safe {
	var cur any
	if current == "" {
		cur = "true"
	}
	items := []any{El("a", At("href", path), At("aria-current", cur), "All")}
	for _, o := range data.Orgs {
		var c any
		if current == o {
			c = "true"
		}
		items = append(items, El("a", OrgDot(o), OrgLabel(o), At("href", path+"?org="+o), At("aria-current", c), Cls("org-"+o)))
	}
	return El("nav", items, Cls("seg seg-links orgseg"), At("aria-label", "Org"))
}

// ShellHeader is the global status header: breadcrumb + h1 left; org filter, Updated hh:mm, health pill and
// Needs-you badge right.
func ShellHeader(title string, shell *ShellView, crumbTrail any, orgNav any, path string) Safe {
	var cluster []any
	if orgNav != nil {
		cluster = append(cluster, orgNav)
	}
	if shell != nil {
		if !shell.Now.IsZero() {
			cluster = append(cluster, El("span", "Updated ", El("time", shell.Now.Format("15:04"), At("datetime", shell.Now.Format("2006-01-02T15:04-07:00"))),
				Cls("updated"), At("title", "refreshes every 60 s")))
		}
		if shell.Health.Tone != "" || shell.Health.Text != "" {
			cluster = append(cluster, El("a", Glyph(healthGlyphOf(shell.Health.Tone)), El("span", shell.Health.Text), At("href", "/resources"),
				Cls("pill hide-phone tone-"+shell.Health.Tone)))
		}
		if shell.Needs != 0 {
			cluster = append(cluster, El("a", fmt.Sprintf("Needs you %d", shell.Needs), At("href", "/#needs-you"),
				At("aria-label", fmt.Sprintf("%d items need you", shell.Needs)), Cls("badge badge--bad hide-phone")))
		}
		if cur, ok := shell.Cursor.(*tokens.DisplayData); ok && cur != nil {
			tone := "success"
			for _, m := range cur.Models {
				if m.Status == "limited" {
					tone = "danger"
				}
			}
			cluster = append(cluster, El("span", tokens.HeaderPill(cur), Cls("pill hide-phone tone-"+tone), At("title", "Cursor usage today")))
		}
	}
	cls := "shellbar"
	if strings.HasPrefix(path, "/task/") {
		cls = "shellbar shellbar--task"
	}
	return El("header", El("div", crumbTrail, El("h1", title), Cls("titlebar")), El("div", cluster, Cls("cluster")), Cls(cls))
}

func healthGlyphOf(tone string) string {
	if g, ok := healthGlyph[tone]; ok {
		return g
	}
	return "exited"
}

func topbar(shell *ShellView, org string) Safe {
	var right []any
	if shell != nil {
		if shell.Needs != 0 {
			right = append(right, El("a", fmt.Sprintf("Needs you %d", shell.Needs), At("href", "/#needs-you"),
				At("aria-label", fmt.Sprintf("%d items need you", shell.Needs)), Cls("badge badge--bad")))
		}
		if shell.Health.Tone != "" || shell.Health.Text != "" {
			right = append(right, El("a", Glyph(healthGlyphOf(shell.Health.Tone)), At("href", "/resources"),
				At("aria-label", shell.Health.Text), Cls("pill pill-dot tone-"+shell.Health.Tone)))
		}
	}
	return El("header", wordmark(org), El("div", right, Cls("topbar-right")), Cls("topbar"))
}

// itemLink is a nav link. Only the Main nav item (cls "nav-item") carries aria-current.
func itemLink(path, label, icon, active string, shell *ShellView, cls string, size int, org string) Safe {
	here := path == active
	mainNav := cls == "nav-item"
	var classes []string
	if cls != "" {
		classes = append(classes, cls)
	}
	if here && mainNav {
		classes = append(classes, "nav-item--active")
	} else if here {
		classes = append(classes, "is-current")
	}
	var count any
	if !(cls == "tab" && path == "/") {
		count = navCount(path, shell)
	}
	var clsAttr, cur any
	if len(classes) > 0 {
		clsAttr = strings.Join(classes, " ")
	}
	if here && mainNav {
		cur = "page"
	}
	return El("a", svg16Of(svg16[icon], size), El("span", label), count, At("href", withOrg(path, org)), At("class", clsAttr), At("aria-current", cur))
}

func shellNavs(active string, shell *ShellView, org string) (side, more, tabs Safe) {
	var groups []any
	for _, g := range Groups {
		var lis []any
		for _, it := range g.Items {
			lis = append(lis, El("li", itemLink(it.Path, it.Label, it.Icon, active, shell, "nav-item", 16, org)))
		}
		groups = append(groups, El("div", El("h2", g.Label), El("ul", lis), Cls("navgroup")))
	}
	side = El("nav", wordmark(org), groups, El("div", At("data-theme-slot", true), Cls("themebox")), At("aria-label", "Main"), Cls("sidebar"))
	flat := map[string]NavItem{}
	for _, g := range Groups {
		for _, it := range g.Items {
			flat[it.Path] = it
		}
	}
	var tabLinks []any
	inTabs := false
	for _, p := range Tabs {
		tabLinks = append(tabLinks, itemLink(p, flat[p].Label, flat[p].Icon, active, shell, "tab", 20, org))
		if p == active {
			inTabs = true
		}
	}
	moreCls := "tab"
	if !inTabs {
		moreCls = "tab is-current"
	}
	tabLinks = append(tabLinks, El("a", svg16Of(svg16["more"], 20), El("span", "More"), At("href", "#more"), Cls(moreCls)))
	var moreItems []any
	for _, g := range Groups {
		var lis []any
		for _, it := range g.Items {
			lis = append(lis, El("li", itemLink(it.Path, it.Label, it.Icon, active, nil, "", 16, org)))
		}
		moreItems = append(moreItems, []any{El("h2", g.Label), El("ul", lis)})
	}
	more = El("nav", moreItems, El("h2", "Theme"), El("div", At("data-theme-slot", true)), At("id", "more"), At("aria-label", "All sections"), Cls("more"))
	tabs = El("nav", tabLinks, At("aria-label", "Primary tabs"), Cls("tabbar"))
	return side, more, tabs
}

// PageOpts are the optional arguments of Page.
type PageOpts struct {
	Active     string // nav entry to mark current; "" means "/"
	NoRefresh  bool   // error pages drop the 60 s meta refresh
	Shell      *ShellView
	CrumbTrail any
	Org        string // the whitelisted ?org= value, "" = all
	Path       string // the request path; turns on the org filter in the header
}

// Page is html.page: the full HTML5 document. body is trusted markup (the output of a view's render).
func Page(title string, body Safe, o PageOpts) string {
	org := o.Org
	if !isOrg(org) {
		org = ""
	}
	active := o.Active
	if active == "" {
		active = "/"
	}
	side, more, tabs := shellNavs(active, o.Shell, org)
	var refresh any = ""
	if !o.NoRefresh {
		refresh = El("meta", At("http-equiv", "refresh"), At("content", "60"))
	}
	head := El("head",
		El("meta", At("charset", "utf-8")),
		El("meta", At("name", "viewport"), At("content", "width=device-width, initial-scale=1")),
		El("meta", At("name", "color-scheme"), At("content", "light dark")),
		refresh,
		El("title", title+" - factory"),
		El("link", At("rel", "icon"), At("href", "/static/favicon.svg"), At("type", "image/svg+xml")),
		El("link", At("rel", "stylesheet"), At("href", "/static/app.css")),
		El("script", At("src", "/static/theme.js"), At("defer", true)))
	var orgNav any
	if o.Path != "" && o.Shell != nil {
		orgNav = OrgFilter(o.Path, org)
	}
	mainEl := El("div", ShellHeader(title, o.Shell, o.CrumbTrail, orgNav, o.Path), El("main", body, At("id", "content"), Cls("page")), more, Cls("main"))
	shellEl := El("div", topbar(o.Shell, org), side, mainEl, Cls("app"))
	return "<!doctype html>\n" + string(El("html", head, El("body", El("a", "Skip to content", At("href", "#content"), Cls("skip")), shellEl, tabs), At("lang", "en")))
}
