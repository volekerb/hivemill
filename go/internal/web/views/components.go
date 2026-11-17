package views

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"factory/go/internal/web/data"
)

// GroupOrder is the status group order of the task list; the last two start collapsed.
var (
	GroupOrder = []string{"blocked", "in_review", "in_progress", "todo", "done", "cancelled"}
	Collapsed  = map[string]bool{"done": true, "cancelled": true}
	prNum      = regexp.MustCompile(`/pull/(\p{Nd}+)\n?\z`)
)

const checkPath = `<path d="M2.5 6.3 5 8.8 9.5 3.6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>`

// KV is one ordered query parameter of Href.
type KV struct{ K, V string }

// pyQuote is urllib.parse.quote(s, safe=","): unreserved characters and the safe set stay, every other UTF-8 byte is %XX.
func pyQuote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~', c == ',':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Href is components.href: path plus the non-empty params, in order, as a query string.
func Href(path string, params ...KV) string {
	var parts []string
	for _, p := range params {
		if p.V != "" {
			parts = append(parts, p.K+"="+pyQuote(p.V))
		}
	}
	if len(parts) == 0 {
		return path
	}
	return path + "?" + strings.Join(parts, "&")
}

// OrgHref is components.org_href: Href with ?org= appended when an org filter is on.
func OrgHref(path, org string, params ...KV) string {
	if isOrg(org) {
		params = append(params, KV{"org", org})
	}
	return Href(path, params...)
}

// Ago is components.ago: "now", "5 m", "3 h", "2 d" (+ suffix); "" when unparseable.
func Ago(iso string, now time.Time, suffix string) string {
	d, ok := data.ParseTS(iso)
	if !ok || now.IsZero() {
		return ""
	}
	s := int64(now.Sub(d).Seconds())
	if s < 0 {
		s = 0
	}
	if s < 60 {
		if suffix == "" {
			return "now"
		}
		return "just now"
	}
	var n int64
	var unit string
	switch {
	case s < 3600:
		n, unit = s/60, "m"
	case s < 86400:
		n, unit = s/3600, "h"
	default:
		n, unit = s/86400, "d"
	}
	return fmt.Sprintf("%d %s%s", n, unit, suffix)
}

// PRLabel is "#226" for a github pull URL, else "PR".
func PRLabel(url string) string {
	if m := prNum.FindStringSubmatch(url); m != nil {
		return "#" + m[1]
	}
	return "PR"
}

// RowStatus is the quiet in-row status: the shape glyph plus the human word as screen-reader text.
func RowStatus(key string) Safe {
	return El("span", Glyph(key), El("span", LabelOf(key), Cls("sr-only")), Cls("st"))
}

// EmptyAction is the optional (href, text) link of an empty state.
type EmptyAction struct{ Href, Text string }

// EmptyState is one calm line inside a section.
func EmptyState(text string, action *EmptyAction) Safe {
	args := []any{text}
	if action != nil {
		args = append(args, " ", El("a", At("href", action.Href), action.Text))
	}
	return El("p", append(args, Cls("empty emptybox"))...)
}

// Disclosure is a <details> with a chevron row. key is the stable data-key (unique per page); count is nil or a value.
func Disclosure(label string, key string, count any, open bool, body ...any) Safe {
	var n any
	if count != nil {
		n = El("span", count, Cls("disc-n"))
	}
	summ := El("summary", El("span", label), n)
	var op any
	if open {
		op = true
	}
	args := append([]any{summ}, body...)
	return El("details", append(args, Cls("disc"), At("data-key", key), At("open", op))...)
}

// Section is a titled box: h2 (+ count) outside, rows inside.
func Section(title string, count any, body ...any) Safe {
	var n any
	if count != nil {
		n = El("span", count, Cls("sect-n"))
	}
	return El("section", El("h2", title, " ", n), El("div", append(body, Cls("sect-box"))...), Cls("sect"))
}

// Chip is one filter chip; Count nil means no count.
type Chip struct {
	Text    string
	Count   *int
	Href    string
	Current bool
}

// N is a pointer to n, for Chip.Count.
func N(n int) *int { return &n }

func chipEl(c Chip) Safe {
	var mark, cnt, cur any
	if c.Current {
		mark = Safe(`<svg class="g" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">` + checkPath + `</svg>`)
		cur = "true"
	}
	cls := "chip"
	if c.Count != nil {
		cnt = El("span", *c.Count, Cls("chip-n"))
		if *c.Count == 0 {
			cls = "chip chip--zero"
		}
	}
	return El("a", mark, El("span", c.Text), " ", cnt, At("href", c.Href), Cls(cls), At("aria-current", cur))
}

// Chips is a labelled wrapping row of chips.
func Chips(label string, items []Chip) Safe {
	var cs []any
	for _, i := range items {
		cs = append(cs, chipEl(i))
	}
	return El("div", El("span", label, Cls("facet-label")), El("div", cs, Cls("chips")), Cls("facet"))
}

// FilterBar is one visible chips row plus the secondary filters in a closed "Filters" disclosure with the count of
// active non-default filters. No rest: no details.
func FilterBar(key string, active int, main []any, rest ...any) Safe {
	var parts []any
	for _, x := range main {
		if x != nil {
			parts = append(parts, x)
		}
	}
	var rs []any
	for _, x := range rest {
		if x != nil {
			rs = append(rs, x)
		}
	}
	if len(rs) > 0 {
		var cnt any
		if active != 0 {
			cnt = active
		}
		parts = append(parts, Disclosure("Filters", key, cnt, false, rs...))
	}
	return El("div", parts...)
}

// SegItem is one option of Segmented.
type SegItem struct {
	Text, Href string
	Current    bool
}

// Segmented is a capsule of exclusive options, links with aria-current on the selected one.
func Segmented(label string, items []SegItem, scrollStrip bool) Safe {
	cls := "seg seg-links"
	if scrollStrip {
		cls += " seg-links--scroll"
	}
	var as []any
	for _, i := range items {
		var cur any
		if i.Current {
			cur = "true"
		}
		as = append(as, El("a", At("href", i.Href), At("aria-current", cur), i.Text))
	}
	return El("nav", as, Cls(cls), At("aria-label", label))
}

// ListRow is a list row (overview, grouped lists): glyph, mono id, title (the one link), flexible gap, right metadata.
func ListRow(glyphKey, ident, title, url string, meta ...string) Safe {
	var t any = El("span", title)
	if url != "" {
		t = El("a", At("href", url), title)
	}
	var id any
	if ident != "" {
		id = El("span", ident, Cls("mono lrow-id"))
	}
	var ms []any
	for _, m := range meta {
		if m != "" {
			ms = append(ms, El("span", m))
		}
	}
	return El("li", RowStatus(glyphKey), id, t, El("span", ms, Cls("lrow-meta")), Cls("lrow"))
}

// Col is one column of DataTable. PhoneLabel turns on the phone card label (PhoneText, or the header label when empty).
type Col struct {
	Key, Label, Cls string
	Sortable        bool
	PhoneLabel      bool
	PhoneText       string
}

// Group is one status group of DataTable.
type Group struct {
	Key, Label string
	Count      int
	Rows       [][]any
	Collapsed  bool
}

// SortState is the effective sort (column key, "asc"|"desc"); nil means unsorted.
type SortState struct{ Key, Dir string }

func isEmptyCell(c any) bool {
	switch v := c.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case Safe:
		return v == ""
	}
	return false
}

func thEl(c Col, sort *SortState, sortHref func(string) string) Safe {
	var cls any
	if c.Cls != "" {
		cls = c.Cls
	}
	if !c.Sortable {
		var lab any = El("span", c.Key, Cls("sr-only"))
		if c.Label != "" {
			lab = c.Label
		}
		return El("th", lab, At("scope", "col"), At("class", cls))
	}
	var arrow any
	if sort != nil && sort.Key == c.Key {
		arrow = "descending"
		if sort.Dir == "asc" {
			arrow = "ascending"
		}
	}
	return El("th", El("a", At("href", sortHref(c.Key)), Cls("sortlink"), c.Label), At("scope", "col"), At("class", cls), At("aria-sort", arrow))
}

func labelled(c Col, cell any) any {
	if !c.PhoneLabel || isEmptyCell(cell) {
		return cell
	}
	text := c.PhoneText
	if text == "" {
		text = c.Label
	}
	return []any{El("span", text, Cls("cl")), cell}
}

func tableEl(cols []Col, groups []Group, sort *SortState, sortHref func(string) string, withHeader bool) Safe {
	var ths []any
	for _, c := range cols {
		ths = append(ths, thEl(c, sort, sortHref))
	}
	head := El("thead", El("tr", ths))
	var bodies []any
	for _, g := range groups {
		var rows []any
		if withHeader && g.Label != "" {
			rows = append(rows, El("tr", El("th", g.Label, " ", El("span", g.Count, Cls("grp-n")), At("colspan", len(cols)), At("scope", "colgroup")), Cls("grp")))
		}
		for _, r := range g.Rows {
			var tds []any
			for i, c := range cols {
				var cell any
				if i < len(r) {
					cell = r[i]
				}
				var cls any
				if c.Cls != "" {
					cls = c.Cls
				}
				tds = append(tds, El("td", labelled(c, cell), At("class", cls)))
			}
			rows = append(rows, El("tr", tds))
		}
		bodies = append(bodies, El("tbody", rows))
	}
	return El("table", head, bodies, Cls("dt"))
}

// DataTable is a real <table> with sortable headers and groups by status. Open groups share one table; collapsed
// groups follow as disclosures keyed key-<group key>. With no rows the empty text shows as an empty state.
func DataTable(cols []Col, groups []Group, sort *SortState, sortHref func(string) string, key, empty string) Safe {
	var gs []Group
	for _, g := range groups {
		if len(g.Rows) > 0 {
			gs = append(gs, g)
		}
	}
	if len(gs) == 0 {
		if empty == "" {
			empty = "Nothing to show."
		}
		return EmptyState(empty, nil)
	}
	if sortHref == nil {
		sortHref = func(string) string { return "#" }
	}
	var open []Group
	for _, g := range gs {
		if !g.Collapsed {
			open = append(open, g)
		}
	}
	var out []any
	if len(open) > 0 {
		out = append(out, tableEl(cols, open, sort, sortHref, true))
	}
	for _, g := range gs {
		if g.Collapsed {
			out = append(out, Disclosure(g.Label, key+"-"+g.Key, g.Count, false, tableEl(cols, []Group{{Rows: g.Rows}}, nil, sortHref, true)))
		}
	}
	cards := false
	for _, c := range cols {
		cards = cards || c.PhoneLabel
	}
	cls := "dt-wrap"
	if cards {
		cls = "dt-wrap dt-wrap--cards"
	}
	return El("div", out, Cls(cls))
}

// FeedRow is a changelog row: Done glyph, title link, goal id, PR "#226" (link), short sha, relative time on the right.
func FeedRow(title, url, goal, prURL, sha, when, whenTitle string) Safe {
	var g, pr, sh, w any
	if goal != "" {
		g = El("span", goal, Cls("feed-goal"))
	}
	if prURL != "" {
		pr = El("span", Link(prURL, PRLabel(prURL)), Cls("mono"))
	}
	if sha != "" {
		r := []rune(sha)
		if len(r) > 8 {
			r = r[:8]
		}
		sh = El("span", string(r), Cls("mono feed-sha"))
	}
	if when != "" {
		w = El("span", when, At("title", whenTitle), Cls("feed-when"))
	}
	return El("li", RowStatus("done"), El("a", At("href", url), title, Cls("feed-title")), g, pr, sh, w, Cls("feed-row"))
}

// Feed is the <ul> wrapper for FeedRow items; modifier ("log") adds the class feed--log.
func Feed(rows []any, modifier string) Safe {
	cls := "feed"
	if modifier != "" {
		cls = "feed feed--" + modifier
	}
	return El("ul", rows, Cls(cls))
}

// AgentCols are the columns of an agent row (components.AGENT_COLS).
var AgentCols = []Col{
	{Key: "status", Label: "", Cls: "c-st"},
	{Key: "agent", Label: "Agent", Cls: "c-title"},
	{Key: "task", Label: "Task", Cls: "c-who", PhoneLabel: true},
	{Key: "last", Label: "Last event", Cls: "c-age num", PhoneLabel: true, PhoneText: "Last"},
}

// RuntimeBadge is the small Claude/Cursor chip. Missing or unknown is Claude (the factory default).
func RuntimeBadge(runtime string) Safe {
	rt := strings.ToLower(pyStrip(runtime))
	if rt == "" {
		rt = "claude"
	}
	if rt != "claude" && rt != "cursor" {
		rt = "claude"
	}
	return El("span", rt, Cls("rt rt-"+rt), At("title", "Agent runtime: "+rt))
}

// OrgBadge is the org chip: "Claude" / "Cursor" in the org's colour, plus a runtime chip only when the runtime
// differs from the org's. Unknown is the default org.
func OrgBadge(org, runtime string) Safe {
	o := org
	if !isOrg(o) {
		o = data.DefaultOrg
	}
	lab := OrgLabel(o)
	chip := El("span", lab, Cls("org org-"+o), At("title", lab+" org"))
	rt := strings.ToLower(pyStrip(runtime))
	if (rt == "claude" || rt == "cursor") && rt != data.OrgRuntime(o) {
		return Safe(string(chip) + " " + string(RuntimeBadge(rt)))
	}
	return chip
}

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

func pyIsSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r') || (r >= 0x1c && r <= 0x1f) || r == 0x85 || unicode.Is(unicode.Zs, r) || r == 0x2028 || r == 0x2029
}

// AgentRow carries what agent_row reads of an agent: name, role, status; the rest optional (empty = absent).
type AgentRow struct {
	Name, Role, Status, Runtime, Org string
	TaskID, TaskTitle, LastEvent     string
	Blocked                          bool
}

// AgentOrg is components.org_of: the agent's org key, else derived from name and runtime.
func AgentOrg(a AgentRow) string {
	if isOrg(a.Org) {
		return a.Org
	}
	rt := ""
	if a.Runtime == "claude" || a.Runtime == "cursor" {
		rt = a.Runtime
	}
	return data.AgentOrg("", a.Name, rt, "")
}

// AgentRowCells are the cells for AgentCols: glyph, name + role (role omitted when equal to the name), task id +
// title, last event age. Blocked gives the Blocked glyph.
func AgentRowCells(a AgentRow, now time.Time) []any {
	key := a.Status
	if a.Blocked {
		key = "blocked"
	}
	var role any
	if a.Role != "" && a.Role != a.Name {
		role = El("span", a.Role, Cls("agent-role"))
	}
	who := El("span", a.Name, " ", role, " ", OrgBadge(AgentOrg(a), a.Runtime), Cls("agent-who"))
	var task any = ""
	if a.TaskID != "" {
		var tt any
		if a.TaskTitle != "" {
			tt = a.TaskTitle
		}
		task = El("span", El("a", At("href", "/task/"+a.TaskID), Cls("mono"), a.TaskID), " ", El("span", tt, At("title", tt), Cls("agent-task")))
	}
	return []any{RowStatus(key), who, task, Ago(a.LastEvent, now, "")}
}

// TileOpts are the optional parts of Tile. DeltaTone defaults to neutral.
type TileOpts struct {
	Unit, Delta, DeltaTone, Definition, Href string
	UnitNL                                   bool
}

// Tile is the C7 metric tile: label, big value (+ small unit), optional delta line and an (i) definition disclosure.
func Tile(label string, value any, o TileOpts) Safe {
	tone := o.DeltaTone
	if tone == "" {
		tone = "neutral"
	}
	var name any = El("span", label)
	if o.Href != "" {
		name = El("a", At("href", o.Href), label)
	}
	var info any
	if o.Definition != "" {
		info = El("details", El("summary", "i", At("aria-label", "What is "+label+"?")), El("p", o.Definition, Cls("tile-def")), Cls("disc--i"))
	}
	var metric Safe
	if o.UnitNL && o.Unit != "" {
		metric = El("div", El("div", value, Cls("metric-val")), El("small", o.Unit, Cls("tile-unit")), Cls("metric metric--stack"))
	} else {
		var u any
		if o.Unit != "" {
			u = El("small", o.Unit)
		}
		metric = El("div", value, u, Cls("metric"))
	}
	var delta any
	if o.Delta != "" {
		delta = El("div", o.Delta, Cls("delta fg-"+tone))
	}
	return El("div", El("div", name, " ", info, Cls("tile-label")), metric, delta, Cls("tile"))
}

var needsGlyph = map[string]string{"danger": "blocked", "warning": "queued", "neutral": "proposed"}

// NeedsItem is one entry of the Needs-you list. Ref is shown once (the subject must not repeat it); Href is the ref's
// page; Message is used only when there is no ref. Escalations carry the subject only, never the body.
type NeedsItem struct {
	Kind, Tone, Subject, Ref, Href string
	Message                        int64
	HasMessage                     bool
	Age                            string
	Org, Goal                      string
}

// NeedsYouRow is the C18 row of the Needs-you list.
func NeedsYouRow(it NeedsItem) Safe {
	tone := it.Tone
	if tone == "" {
		tone = "neutral"
	}
	subject := it.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	var ident any
	switch {
	case it.Ref != "":
		if it.Href != "" {
			ident = El("a", it.Ref, At("href", it.Href), Cls("mono nid"))
		} else {
			ident = El("span", it.Ref, Cls("mono nid"))
		}
	case it.HasMessage:
		ident = El("span", fmt.Sprintf("Message #%d", it.Message), Cls("mono nid"))
	}
	g, ok := needsGlyph[tone]
	if !ok {
		g = "proposed"
	}
	var age any
	if it.Age != "" {
		age = El("span", it.Age, Cls("nage"))
	}
	return El("li", Glyph(g), El("span", it.Kind, Cls("nkind fg-"+tone)), El("span", subject, At("title", subject), Cls("nsub")), ident, age, Cls("needs"))
}
