package views

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/web/data"
)

// TestRoutesMatchPython: the route keys of part 1 equal the matching keys of web/views/__init__.py ROUTES; the keys it
// does not own are exactly the part-2 pages (T-1476), so a route added in Python without a Go owner fails here.
func TestRoutesMatchPython(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "web", "views", "__init__.py")) // the real file: the merge gate has it
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s+"(/[^"]*)":\s*(\w+)`)
	var part1, rest []string
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		switch m[2] {
		case "home", "goals", "tasks":
			part1 = append(part1, m[1])
		default:
			rest = append(rest, m[1])
		}
	}
	sort.Strings(part1)
	sort.Strings(rest)
	if got := RouteKeys(); !reflect.DeepEqual(got, part1) {
		t.Errorf("Go routes %v != Python part-1 routes %v", got, part1)
	}
	wantRest := []string{"/agents", "/changelog", "/quality", "/resources", "/throughput", "/tokens"}
	if !reflect.DeepEqual(rest, wantRest) {
		t.Errorf("Python routes outside part 1 are %v, want %v (a new route needs an owner)", rest, wantRest)
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		path, active, id string
		ok               bool
	}{
		{"/", "/", "", true}, {"/goals", "/goals", "", true}, {"/tasks", "/tasks", "", true},
		{"/goal/G-048", "/goals", "G-048", true}, {"/task/T-1475", "/tasks", "T-1475", true},
		{"/task/T-\u0663", "/tasks", "T-\u0663", true}, // Python \d matches U+0663
		{"/task/T-1\n", "/tasks", "T-1", true},         // Python $ allows one trailing newline
		{"/goal/<id>", "/goal/<id>", "", true},         // the literal route key resolves like ROUTES.get(path)
		{"/task/t-1", "", "", false}, {"/task/T-", "", "", false}, {"/task/T-1/x", "", "", false}, {"/agents", "", "", false},
		{"/goal/GG-1", "", "", false}, {"", "", "", false},
	} {
		v, params, active, ok := Match(c.path)
		if ok != c.ok || active != c.active || params["id"] != c.id || (ok && v == nil) {
			t.Errorf("Match(%q) = %v, %v, %q, %v; want %q id %q ok %v", c.path, v, params, active, ok, c.active, c.id, c.ok)
		}
	}
}

func TestElEscapesAndRejectsBadNames(t *testing.T) {
	got := string(El("a", At("href", `/x"><script>`), At("title", nil), At("hidden", true), At("gone", false), `<b>&'"`, Safe("<i>ok</i>"), nil, false))
	want := `<a href="/x&#34;&gt;&lt;script&gt;" hidden>&lt;b&gt;&amp;&#39;&#34;<i>ok</i></a>`
	if got != want {
		t.Errorf("El = %s\nwant %s", got, want)
	}
	for _, f := range []func(){
		func() { El("A") }, func() { El("a b") }, func() { El("a", At("onClick", "x")) }, func() { El("a", At("on_click", "x")) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic for a bad tag or attribute name")
				}
			}()
			f()
		}()
	}
	if got := string(El("meta", At("charset", "utf-8"), "ignored?")); got != `<meta charset="utf-8">` {
		t.Errorf("void tag = %s", got)
	}
}

// TestUnicodeParity: Go regexp \s \d \w and strings.TrimSpace are not Python's. Expected values are the output of the
// Python function on the same input (python3 -I, repo root on sys.path), quoted in the comments.
func TestUnicodeParity(t *testing.T) {
	// queries._strip_tags("a\u00a0[SC1]\vb") == 'a b'; queries._strip_tags("x [SC\u0663, SC2] y") == 'x y'
	if got := stripTags("a\u00a0[SC1]\vb"); got != "a b" {
		t.Errorf("stripTags NBSP/VT = %q, want %q", got, "a b")
	}
	if got := stripTags("x [SC\u0663, SC2] y"); got != "x y" {
		t.Errorf("stripTags U+0663 = %q, want %q", got, "x y")
	}
	// html.link("/p\u0663", "x") == <a href="/p٣">x</a>; link("/a\u00a0b", "x") == x (NBSP is not \w)
	if got := string(Link("/p\u0663", "x")); got != `<a href="/p٣">x</a>` {
		t.Errorf("Link U+0663 = %s", got)
	}
	if got := string(Link("/a\u00a0b", "x")); got != "x" {
		t.Errorf("Link NBSP = %s", got)
	}
	// link("//evil", "x") == x; link("https://github.com/o/r/pull/12\n", "x") keeps the anchor ($ before a newline)
	if got := string(Link("//evil", "x")); got != "x" {
		t.Errorf("Link // = %s", got)
	}
	if got := string(Link("https://github.com/o/r/pull/12\n", "x")); got != "<a href=\"https://github.com/o/r/pull/12\n\">x</a>" {
		t.Errorf("Link PR newline = %q", got)
	}
	// int(pr_label(".../pull/\u0663\u0661")[1:]) == 31; pr_label(...) == '#٣١'; pr_label("x/pull/5\n") == '#5'
	if got, n := PRLabel("https://github.com/o/r/pull/\u0663\u0661"), prNumber("https://github.com/o/r/pull/\u0663\u0661"); got != "#٣١" || n != 31 {
		t.Errorf("PRLabel/prNumber = %q, %d; want #٣١, 31", got, n)
	}
	if got := PRLabel("x/pull/5\n"); got != "#5" {
		t.Errorf("PRLabel newline = %q", got)
	}
	// _TASK_RE.findall("T-\u0663 and\u00a0T-12 aT-9 T-9b T-7\vT-7") == ['T-٣', 'T-12', 'T-7', 'T-7'] (sorted set here)
	if got, want := taskRefs("T-\u0663 and\u00a0T-12 aT-9 T-9b T-7\vT-7"), []string{"T-12", "T-7", "T-\u0663"}; !reflect.DeepEqual(got, want) {
		t.Errorf("taskRefs = %q, want %q", got, want)
	}
	// _PR_SET_RE.search: "x\u00e9pr=" False, "\u00a0pr=" True, "status=in_review\u0663" False, "\u00a0status=in_review\v" True
	for in, want := range map[string]bool{"x\u00e9pr=": false, "\u00a0pr=": true, "status=in_review\u0663": false, "\u00a0status=in_review\v": true, "pr=": true, "spr=": false} {
		if got := prSet(in); got != want {
			t.Errorf("prSet(%q) = %v, want %v", in, got, want)
		}
	}
	// " ".join("a\u00a0\vb\u0085c\x1fd".split()) == 'a b c d'
	if text, _ := NormalizeDOM("<p>a\u00a0\vb\u0085c\x1fd</p>"); text != "a b c d" {
		t.Errorf("NormalizeDOM whitespace = %q, want %q", text, "a b c d")
	}
	// components.href("/tasks", {"goal": "G 1", "x": "é/,&"}) == '/tasks?goal=G%201&x=%C3%A9%2F,%26'
	if got := Href("/tasks", KV{"goal", "G 1"}, KV{"x", "é/,&"}, KV{"empty", ""}); got != "/tasks?goal=G%201&x=%C3%A9%2F,%26" {
		t.Errorf("Href = %s", got)
	}
}

func TestPyFormats(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{money(1234.5), "$1,234.50"}, {money(0.004), "$0.00"}, {money(-12.345), "$-12.35"}, // the dollar sign comes first, then the minus
		{commas(1234567), "1,234,567"}, {commas(-1000), "-1,000"}, {commas(12), "12"},
		{fmtG(0), "0"}, {fmtG(2.5), "2.5"}, {fmtG(12), "12"}, // f"{v:g}"
		{pct0(0.125), "12%"}, {pct0(0.5), "50%"}, {pct0(1), "100%"}, // f"{v:.0%}": 0.125 -> '12%'
		{Dur(59.4, true), "59s"}, {Dur(61, true), "1m 01s"}, {Dur(3600, true), "1h 00m"}, {Dur(90000, true), "1d 1h"}, {Dur(0, false), "-"},
		{Dur(2.5, true), "2s"}, {Dur(3.5, true), "4s"}, // round half to even
		{Compact(999), "999"}, {Compact(1500), "1.5k"}, {Compact(999999), "1.0M"}, {Compact(48567200), "48.6M"}, {Compact(0), "0"},
		{exact(48567200), "48,567,200"}, {exact(1.5), "1.5"},
		{pyFloat(22), "22.0"}, {pyFloat(2.5), "2.5"},
		{Ago("2026-11-14T10:59:59+00:00", testNow, ""), "now"}, {Ago("2026-11-14T10:59:59+00:00", testNow, " ago"), "just now"},
		{Ago("2026-11-14T10:00:00+00:00", testNow, ""), "1 h"}, {Ago("2026-11-12T10:00:00+00:00", testNow, " ago"), "2 d ago"},
		{Ago("garbage", testNow, ""), ""}, {Ago("2026-11-14T12:00:00+00:00", testNow, ""), "now"}, // a future stamp clamps to now
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if got := Ts("2026-11-14T12:00:00+01:00"); got != `<span title="2026-11-14T12:00:00+01:00">11-14 11:00</span>` {
		t.Errorf("Ts = %s", got)
	}
	if got := Ts("nope"); got != "nope" {
		t.Errorf("Ts garbage = %s", got)
	}
}

func TestChartsShape(t *testing.T) {
	m := string(Meter(3, 9, "3/9 tasks", "tasks"))
	for _, want := range []string{`aria-label="3 of 9 tasks done"`, `width="33.3"`, `class="lbl">3/9 tasks`} {
		if !strings.Contains(m, want) {
			t.Errorf("Meter lacks %s: %s", want, m)
		}
	}
	if !strings.Contains(string(Meter(0, 0, "0/0 tasks", "tasks")), `width="0.0"`) {
		t.Error("Meter with no tasks must have an empty fill")
	}
	g := string(GaugeBar(150, []float64{92, -5}, "bogus", "x"))
	for _, want := range []string{`class="fill-success"`, `width="100.0"`, `x1="92.0"`, `x1="0.0"`} {
		if !strings.Contains(g, want) {
			t.Errorf("GaugeBar lacks %s: %s", want, g)
		}
	}
	if got := string(Columns(nil, "merges", 96, "day")); !strings.Contains(got, "No merges in this range") {
		t.Errorf("Columns empty = %s", got)
	}
	if got := string(Columns([]Point{{"2026-11-01", 0}}, "merges", 96, "day")); !strings.Contains(got, "No merges in this range") {
		t.Errorf("Columns all zero = %s", got)
	}
	c := string(Columns([]Point{{"2026-11-01", 3}, {"2026-11-02", 0}, {"2026-11-03", 48567200}}, "tokens", 96, "day"))
	for _, want := range []string{`peak 48.6M on 11-03`, `48,567,200`, `text-anchor="start"`} {
		if !strings.Contains(c, want) {
			t.Errorf("Columns lacks %s: %s", want, c)
		}
	}
	s := string(Sparkline([]Point{{"2026-11-01", 1}, {"2026-11-02", 3}}, "merges", "per day"))
	if !strings.Contains(s, `aria-label="4 merges in 2 days, peak 3 on 11-02"`) || !strings.Contains(s, `class="spark-last"`) {
		t.Errorf("Sparkline = %s", s)
	}
	if !strings.Contains(string(Sparkline(nil, "merges", "per day")), `0 merges in 0 days`) {
		t.Error("empty Sparkline must still describe itself")
	}
}

// TestStepText covers the structured detail of a timeline step: never free text beyond the 120-char judge reason.
func TestStepText(t *testing.T) {
	sp := func(s string) *string { return &s }
	for _, c := range []struct {
		in         data.CycleItem
		text, sha  string
		tone       string
		wantVerb   string
		wantRejSet bool
	}{
		{data.CycleItem{Kind: "event", Label: "update", Detail: sp("status=in_progress x")}, "status -> in_progress", "", "muted", "Status -> in_progress", false},
		{data.CycleItem{Kind: "event", Label: "update", Detail: sp("owner note")}, "update", "", "muted", "Update", false},
		{data.CycleItem{Kind: "event", Label: "merged", Detail: sp("@abc1234ffff")}, "merged", "abc1234f", "ok", "Merged", false},
		{data.CycleItem{Kind: "event", Label: "merged", Detail: sp("not a sha")}, "merged", "", "ok", "Merged", false},
		{data.CycleItem{Kind: "event", Label: "merge-post-error", Detail: sp("deadbeef")}, "merge post-step error", "deadbeef", "muted", "Merge post-step error", false},
		{data.CycleItem{Kind: "event", Label: "judge-blocked", Detail: sp(strings.Repeat("x", 200))}, "judge blocked: " + strings.Repeat("x", 119) + "…", "", "bad", "Judge blocked: " + strings.Repeat("x", 119) + "…", false},
		{data.CycleItem{Kind: "verdict", Label: "qa fail", Head: sp("1234")}, "qa: fail (fail)", "1234", "bad", "QA failed", true},
		{data.CycleItem{Kind: "verdict", Label: "judge merge"}, "judge: pass (merge)", "", "ok", "Judge merged", false},
		{data.CycleItem{Kind: "agent", Label: "spawn coder"}, "spawn coder", "", "muted", "Coder started", false},
		{data.CycleItem{Kind: "agent", Label: "end reviewer"}, "end reviewer", "", "muted", "Reviewer finished", false},
		{data.CycleItem{Kind: "event", Label: "add", Detail: sp("title=secret")}, "task created", "", "muted", "Task created", false},
		{data.CycleItem{Kind: "event", Label: "judge-reject"}, "judge-reject", "", "bad", "Judge rejected", true},
	} {
		text, sha := stepText(c.in)
		s := step{Kind: c.in.Kind, Label: c.in.Label, Text: text}
		if text != c.text || sha != c.sha || failKind(c.in) != c.tone || verb(s) != c.wantVerb || isReject(s) != c.wantRejSet {
			t.Errorf("%s %q: got (%q, %q, %s, %q, reject %v), want (%q, %q, %s, %q, reject %v)", c.in.Kind, c.in.Label, text, sha, failKind(c.in),
				verb(s), isReject(s), c.text, c.sha, c.tone, c.wantVerb, c.wantRejSet)
		}
	}
}

func TestNormalizeDOM(t *testing.T) {
	doc := `<!doctype html><html><head><title>T &amp; x</title><script>var a = "<b>";</script><style>p{}</style></head>` +
		`<body><!-- c --><a href="/goals?org=claude&amp;x=1" title="a>b">Go  <b>al</b></a><p id=main>11-14 12:00 Updated 11:00 x</p><br/><a href='/t'>t</a><a name=x>n</a></body></html>`
	text, links := NormalizeDOM(doc)
	if !strings.Contains(text, "T & x") || strings.Contains(text, "var a") || strings.Contains(text, "p{}") || strings.Contains(text, "<b>") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "<TS> <TS> x") {
		t.Errorf("timestamps must be masked: %q", text)
	}
	if want := []string{"#main", "a:/goals?org=claude&x=1", "a:/t"}; !reflect.DeepEqual(links, want) {
		t.Errorf("links = %q, want %q", links, want)
	}
}

func FuzzNormalizeDOM(f *testing.F) {
	c := newTestDB(f)
	for _, p := range []string{"/", "/goals", "/tasks", "/goal/G-001", "/task/T-2"} {
		f.Add(renderPage(f, c, p, map[string]string{}))
	}
	f.Add(`<a href="x`)
	f.Add("<!-- never closed")
	f.Add(`<p title='a>b'>t</p><script>x`)
	f.Fuzz(func(t *testing.T, doc string) {
		text, links := NormalizeDOM(doc)
		if strings.Contains(text, "  ") || text != strings.TrimSpace(text) {
			t.Errorf("text not collapsed: %q", text)
		}
		if !sort.StringsAreSorted(links) {
			t.Errorf("links not sorted: %q", links)
		}
	})
}

func FuzzMatchAndParsers(f *testing.F) {
	for _, s := range []string{"/", "/goal/G-1", "/task/T-\u0663\n", "/task/T-1/x", "status=in_review pr=x", "T-1 T-2b aT-3", "@abc1234", "/pull/5\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if v, params, _, ok := Match(s); ok && (v == nil || params == nil) {
			t.Errorf("Match(%q) ok with nil view or params", s)
		}
		_ = prSet(s)
		_ = taskRefs(s)
		_ = prNumber(s)
		_ = PRLabel(s)
		_ = pyQuote(s)
		_ = stripTags(s)
		_ = Ago(s, time.Unix(0, 0), "")
		_ = string(Link(s, s))
		text, sha := stepText(data.CycleItem{Kind: "event", Label: "merged", Detail: &s})
		if len(sha) > 8 || text == "" {
			t.Errorf("stepText(%q) = %q, %q", s, text, sha)
		}
	})
}
