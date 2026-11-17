package views

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The expectations below were taken from Python 3.13.5 (int(), float(), re, str.strip, os.path.realpath).

func TestUnicodeNumberParsing(t *testing.T) {
	ints := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"٣", 3, true},         // int("٣") == 3
		{" 7 ", 7, true},       // NBSP is skipped
		{"\x0b7\x0b", 7, true}, // \v is skipped
		{"1_0", 10, true},      // a single underscore between digits
		{"1__0", 0, false},     // ValueError
		{"_1", 0, false},       // ValueError
		{"1_", 0, false},       // ValueError
		{"+٣", 3, true},        // sign, then a Unicode digit
		{"", 0, false},         // ValueError
		{"١٢٣", 123, true},     // ١٢٣
		{"²", 0, false},        // a superscript digit is not a decimal digit: ValueError
		{"7\x1c", 0, false},    // U+001C is not skipped by int() although str.strip() removes it
	}
	for _, c := range ints {
		got, ok := pyIntText(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("pyIntText(%q) = %d, %v; Python gives %d ok=%v", c.in, got, ok, c.want, c.ok)
		}
	}
	floats := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"1_0.5", 10.5, true}, {"1.5e3", 1500, true}, {"١.5", 1.5, true}, {" 2.5 ", 2.5, true}, {"1e999", math.Inf(1), true},
		{"0x1p3", 0, false}, {"1._5", 0, false}, {"1e", 0, false}, {"", 0, false}, {"2.5\x1c", 0, false},
	}
	for _, c := range floats {
		got, ok := pyFloatText(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("pyFloatText(%q) = %v, %v; Python gives %v ok=%v", c.in, got, ok, c.want, c.ok)
		}
	}
	if f, ok := pyFloatText("nan"); !ok || !math.IsNaN(f) {
		t.Errorf("pyFloatText(nan) = %v, %v; Python gives nan", f, ok)
	}
}

func TestUnicodeAgentFilters(t *testing.T) {
	// Python: re.match(r"^G-\d+$", "G-٣") and "G-1\n" match, "G-²" does not; " live\x0b".strip() == "live".
	q := map[string]string{"status": " live\x0b,queued,live", "role": "coder ", "goal": "G-٣, G-²,G-1", "runtime": " claude "}
	f := parseAgentFilters(q, []string{"live", "queued"})
	if got := strings.Join(f.Status, "|"); got != "live|queued" {
		t.Errorf("status = %q, want live|queued (NBSP and \\v stripped, duplicate dropped)", got)
	}
	if got := strings.Join(f.Role, "|"); got != "coder" {
		t.Errorf("role = %q, want coder", got)
	}
	if got := strings.Join(f.Goal, "|"); got != "G-٣|G-1" {
		t.Errorf("goal = %q, want G-٣|G-1 (a superscript digit is not \\d)", got)
	}
	if got := strings.Join(f.Runtime, "|"); got != "claude" {
		t.Errorf("runtime = %q, want claude", got)
	}
	for in, want := range map[string]bool{"G-٣": true, "G-1\n": true, "G-²": false, "G-": false, "xG-1": false} {
		if got := agentGoalRe.MatchString(in); got != want {
			t.Errorf("agentGoalRe(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]bool{"G-٣": true, "unattributed": true, "unattributed\n": true, "G-²": false, "unattributedx": false} {
		if got := tokenGoalRe.MatchString(in); got != want {
			t.Errorf("tokenGoalRe(%q) = %v, want %v", in, got, want)
		}
	}
	if got := changelogGoalRe.MatchString("G-٣"); !got {
		t.Error("changelogGoalRe must accept G-\\u0663")
	}
	if n := throughputDaysOf(&Ctx{Query: map[string]string{"days": " ٧ "}}); n != 7 { // int(" ٧\xa0") == 7
		t.Errorf("days=' ٧ ' gives %d, want 7", n)
	}
	if n := throughputDaysOf(&Ctx{Query: map[string]string{"days": "1_4"}}); n != 14 { // int("1_4") == 14
		t.Errorf("days=1_4 gives %d, want 14", n)
	}
	if n := throughputDaysOf(&Ctx{Query: map[string]string{"days": "8"}}); n != defaultDays {
		t.Errorf("days=8 gives %d, want the default %d", n, defaultDays)
	}
}

// TestPyRealpathMatchesPython: a dangling symlink, a chain through a missing component and loops resolve like
// os.path.realpath, never as an error.
func TestPyRealpathMatchesPython(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir()) // the temp dir itself may sit behind a symlink
	if err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{"d": "missing", "c": "gone/sub", "c2": "c", "l": "l", "a": "b", "b": "a", "rx": "real/../real/x", "abs1": "/nonexistent_abs"} {
		if err := os.Symlink(dst, filepath.Join(root, src)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"d": "missing", "c": "gone/sub", "c2": "gone/sub", "l": "l", "a": "a", "b": "b", "rx": "real/x", "abs1": "/nonexistent_abs",
		"d/x/y": "missing/x/y", "l/x": "l/x", "real/../d": "missing",
	} {
		got := pyRealpath(filepath.Join(root, in))
		if strings.HasPrefix(want, "/") {
			if got != want {
				t.Errorf("realpath(%s) = %s, Python gives %s", in, got, want)
			}
		} else if rel := strings.TrimPrefix(got, root+"/"); rel != want {
			t.Errorf("realpath(%s) = %s, Python gives %s", in, rel, want)
		}
	}
}

func writeProc(t *testing.T, root, pid, cmdline, stat string) {
	t.Helper()
	d := filepath.Join(root, pid)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if cmdline != "" {
		if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if stat != "" {
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeProc(t *testing.T) string {
	root := t.TempDir()
	writeProc(t, root, "101", "/usr/bin/claude\x00--session-id\x00s1\x00", "101 (claude) S 1 101 101 0")
	writeProc(t, root, "102", "node\x00/opt/bin/claude\x00", "102 (node (x)) S 1 102 102 0") // claude as the 2nd arg; ")" inside the name
	writeProc(t, root, "103", "claude\x00--mcp\x00", "103 (claude) S 101 103 103 0")         // a child of 101: not top-level
	writeProc(t, root, "104", "bash\x00-c\x00x\x00claude\x00", "104 (bash) S 1 104 104 0")   // claude is the 4th arg: not matched
	writeProc(t, root, "105", "", "105 (claude) S 1 1 1 0")                                  // no cmdline: skipped
	writeProc(t, root, "106", "claude\x00", "")                                              // no stat: the parent is unknown, counted top-level
	writeProc(t, root, "107", "claude\x00", "garbage without a paren")                       // unparsable stat: the same
	if err := os.MkdirAll(filepath.Join(root, "notapid"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProcScanLikePython(t *testing.T) {
	snap := scanProcs(fakeProc(t))
	var pids []int64
	for p := range snap {
		pids = append(pids, p)
	}
	if len(snap) != 5 || snap[101].PPID == nil || *snap[101].PPID != 1 || snap[103].PPID == nil || *snap[103].PPID != 101 || snap[106].PPID != nil || snap[107].PPID != nil {
		t.Fatalf("scan found %v (want 101, 102, 103, 106, 107 with their parents)", pids)
	}
	if n, ok := ClaudeTotal(snap); !ok || n != 4 { // 101, 102, 106, 107: 103 is a child of 101
		t.Errorf("ClaudeTotal = %d, %v; want 4", n, ok)
	}
	if _, ok := ClaudeTotal(nil); ok {
		t.Error("ClaudeTotal(nil) must be unknown")
	}
	if got := scanProcs(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Errorf("an unreadable /proc must give a nil snapshot (unknown), got %v", got)
	}
}

// TestProcScanBudgetAndTTL: the scan stops after PROC_BUDGET_S and caches the partial result for PROC_TTL_S.
func TestProcScanBudgetAndTTL(t *testing.T) {
	root := fakeProc(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	step := time.Duration(0)
	oldClock, oldHave, oldVal, oldAt := procClock, procHave, procValue, procAt
	t.Cleanup(func() { procClock, procHave, procValue, procAt = oldClock, oldHave, oldVal, oldAt })
	procClock = func() time.Time { now = now.Add(step); return now }
	if procBudget != 80*time.Millisecond || procTTL != 30*time.Second {
		t.Fatalf("budget %v and ttl %v must be agents.py PROC_BUDGET_S=0.08 and PROC_TTL_S=30", procBudget, procTTL)
	}
	step = time.Second // every clock read is a second later: the budget is over at the first entry
	if got := scanProcs(root); got == nil || len(got) != 0 {
		t.Errorf("a scan out of budget returns the (empty) partial result, got %v", got)
	}
	step = 0
	t.Setenv("FACTORY_PROC_DIR", root)
	t.Setenv("FACTORY_HOME", t.TempDir()) // not the live home: the fake /proc is used
	procHave = false
	if n, _ := ClaudeTotal(cachedProcScan()); n != 4 {
		t.Fatalf("first snapshot counts %d, want 4", n)
	}
	for _, pid := range []string{"101", "102", "106", "107"} {
		if err := os.RemoveAll(filepath.Join(root, pid)); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(29 * time.Second)
	if n, _ := ClaudeTotal(cachedProcScan()); n != 4 {
		t.Errorf("within the TTL the cached snapshot is served, got %d", n)
	}
	now = now.Add(2 * time.Second)
	if n, _ := ClaudeTotal(cachedProcScan()); n != 1 { // only 103 is left; its parent is gone, so it counts top-level
		t.Errorf("after the TTL the snapshot is rescanned, got %d", n)
	}
}

func TestProcDirRule(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("FACTORY_PROC_DIR", fake)
	t.Setenv("FACTORY_HOME", t.TempDir())
	if got := procDir(); got != fake {
		t.Errorf("a non-live FACTORY_HOME uses the fake proc dir, got %s", got)
	}
	for h := range liveHomes() {
		t.Setenv("FACTORY_HOME", h)
		if got := procDir(); got != "/proc" {
			t.Errorf("the live home must read the real /proc, got %s", got)
		}
	}
	t.Setenv("FACTORY_HOME", "")
	if got := procDir(); got != "/proc" {
		t.Errorf("without FACTORY_HOME the live home is assumed: /proc, got %s", got)
	}
	t.Setenv("FACTORY_PROC_DIR", "")
	if got := procDir(); got != "/proc" {
		t.Errorf("no fake dir: /proc, got %s", got)
	}
}

func TestBlockedSinceHHMM(t *testing.T) { // the test zone is UTC (TestMain)
	for _, c := range []struct {
		b    blockedSince
		want string
	}{
		{blockedSince{Set: true, IsNum: true, Num: 1794650400}, "10:00"},
		{blockedSince{Set: true, Text: " 1794650400.5 "}, "10:00"},
		{blockedSince{Set: true, Text: "not a number"}, ""},
		{blockedSince{Set: true, IsNum: true, Num: math.NaN()}, ""},
		{blockedSince{Set: true, IsNum: true, Num: 1e20}, ""},
		{blockedSince{Set: true, IsNum: true, Num: -1}, "23:59"},
	} {
		if got := c.b.hhmm(); got != c.want {
			t.Errorf("hhmm(%+v) = %q, want %q", c.b, got, c.want)
		}
	}
	for b, want := range map[blockedSince]bool{{}: false, {Set: true, Text: ""}: false, {Set: true, Text: "x"}: true, {Set: true, IsNum: true}: true} {
		if got := b.isBlocked(); got != want {
			t.Errorf("isBlocked(%+v) = %v, want %v", b, got, want)
		}
	}
}

func TestAgentsHref(t *testing.T) {
	f := agentFilters{Status: []string{"live", "exited"}, Goal: []string{"G-٣"}}
	if got, want := agentsHref(f, "claude", nil), "/agents?status=live%2Cexited&goal=G-%D9%A3&org=claude"; got != want {
		t.Errorf("href = %s, want %s", got, want)
	}
	if got, want := agentsHref(f, "bogus", map[string][]string{"status": nil, "role": {"coder"}}), "/agents?role=coder&goal=G-%D9%A3"; got != want {
		t.Errorf("href = %s, want %s", got, want)
	}
	if got := agentsHref(agentFilters{}, "", nil); got != "/agents" {
		t.Errorf("href = %s, want /agents", got)
	}
}

// TestTimelinePaging: following "More" visits every item once, newest first, 40 per page, and ends.
func TestTimelinePaging(t *testing.T) {
	c := newPart2DB(t)
	var before *[3]int64
	seen := map[string]bool{}
	var lastKey [3]int64
	pages, total := 0, 0
	for {
		items, next, err := timeline(c, testNow, before)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > resPageSize || (next != "" && len(items) != resPageSize) {
			t.Fatalf("page %d has %d items (next %q)", pages, len(items), next)
		}
		for _, i := range items {
			key := fmt.Sprintf("%d-%d-%d", i.Epoch, i.Src, i.ID)
			if seen[key] {
				t.Fatalf("item %s shown twice", key)
			}
			seen[key] = true
			k := [3]int64{i.Epoch, int64(i.Src), i.ID}
			if total > 0 && !(k[0] < lastKey[0] || (k[0] == lastKey[0] && (k[1] < lastKey[1] || (k[1] == lastKey[1] && k[2] < lastKey[2])))) {
				t.Fatalf("item %s is not older than the one before", key)
			}
			lastKey = k
			total++
		}
		pages++
		if next == "" {
			break
		}
		if before = parseBefore(next); before == nil {
			t.Fatalf("the cursor %q does not parse", next)
		}
		if pages > 10 {
			t.Fatal("paging does not end")
		}
	}
	if pages < 2 || total <= resPageSize {
		t.Errorf("the fixture should need several pages, got %d pages and %d items", pages, total)
	}
	for _, bad := range []string{"", "1-2-3", "1-0-", "x-0-1", "1-0-1x", "1234567890123-0-1", "٣1-0-1", "1-0-1\n"} {
		if parseBefore(bad) != nil {
			t.Errorf("parseBefore(%q) must be nil (Python re.fullmatch of [0-9]{1,12}-[01]-[0-9]{1,12})", bad)
		}
	}
}

// TestNonFiniteGateConfigErrors: Python's int(float("nan")) raises, so the page is an error, not a guess.
func TestNonFiniteGateConfigErrors(t *testing.T) {
	sql := filepath.Join(t.TempDir(), "nan.sql")
	if err := os.WriteFile(sql, []byte("UPDATE config SET value='nan' WHERE key='max_live_agents';"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestDB(t, filepath.Join("testdata", "part2.sql"), sql)
	_, _, err := resourcesView{}.Render(newTestCtx(c, nil), nil)
	if err == nil || !strings.Contains(err.Error(), "max_live_agents") {
		t.Errorf("Render err = %v, want one naming max_live_agents", err)
	}
}

func TestCursorActivity(t *testing.T) {
	c := newPart2DB(t)
	ag, err := c.Agents(true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	rows := cursorActivity(ag, "2026-11-14", testNow, "")
	// cursor-coder-t-6 (live since 10:30Z: 0.5 h), cursor-judge-t-22 (08:00-09:00+01:00: 1 h, ended today), the cursor
	// org's claude-runtime evaluator is not a cursor-runtime agent
	want := map[string]CursorAct{"composer-2.5": {Org: "cursor", Model: "composer-2.5", Sessions: 1, Live: 1, Hours: 0.5}, "grok-4.7-high": {Org: "cursor", Model: "grok-4.7-high", Sessions: 1, Hours: 1}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for _, r := range rows {
		if w := want[r.Model]; r != w {
			t.Errorf("row %+v, want %+v", r, w)
		}
	}
	if rows[0].Model != "grok-4.7-high" { // longest hours first
		t.Errorf("rows are sorted by hours descending: %+v", rows)
	}
	if got := cursorActivity(ag, "2026-11-14", testNow, "claude"); len(got) != 0 {
		t.Errorf("the org filter leaves no cursor agents of the claude org, got %+v", got)
	}
	if got := cursorActivity(ag, "2026-11-15", testNow, ""); len(got) != 0 { // every agent ended or still live before the start: end < since drops it
		t.Errorf("since after every agent's end: %+v", got)
	}
	if got := cursorActivity(ag, "", testNow, ""); len(got) != 3 { // all time also takes the legacy cursor-goal agent (11-10)
		t.Errorf("all time: %+v", got)
	}
}

func TestStatHelpers(t *testing.T) {
	if p := parseStatPPid("1 (a) S 7 1"); p == nil || *p != 7 {
		t.Errorf("parseStatPPid = %v, want 7", p)
	}
	if p := parseStatPPid("1 (a) S ٣ 1"); p == nil || *p != 3 { // int("٣") == 3
		t.Errorf("parseStatPPid with a Unicode digit = %v, want 3", p)
	}
	for _, bad := range []string{"", "no paren", "1 (a)", "1 (a) S", "1 (a) S x 1"} {
		if parseStatPPid(bad) != nil {
			t.Errorf("parseStatPPid(%q) must be nil", bad)
		}
	}
	if !cmdlineIsClaude([]byte("/x/claude\x00")) || cmdlineIsClaude([]byte("a\x00b\x00c\x00claude\x00")) || cmdlineIsClaude([]byte("\x00\x00claude-x")) {
		t.Error("cmdlineIsClaude: basename of the first three non-empty arguments")
	}
}

// ── fuzz ──

func FuzzProcParsers(f *testing.F) {
	for _, s := range []string{"101 (claude) S 1 101", "1 (a (b)) S 7", "", ")", "\x00\x00", "claude\x00--x\x00", "/a/b/claude", "٣ (x) ٣ ٤"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		parseStatPPid(s)
		cmdlineIsClaude([]byte(s))
		pyFields(s)
	})
}

func FuzzPyNumberText(f *testing.F) {
	for _, s := range []string{"1", "-7", "1_0", "٣", " 7 ", "1e5", "nan", "inf", "0x1p3", "1__0", "", "+", "_", "1e999", ".5", "5."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, ok := pyIntText(s); ok {
			_ = n
		}
		if v, ok := pyFloatText(s); ok && math.IsNaN(v) && !strings.Contains(strings.ToLower(s), "nan") {
			t.Errorf("pyFloatText(%q) is NaN without a nan spelling", s)
		}
	})
}

func FuzzParseBefore(f *testing.F) {
	for _, s := range []string{"1794568920-1-73", "1-0-1", "", "x", "12-3-4", "123456789012-1-123456789012", "-1-1-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if b := parseBefore(s); b != nil && (b[1] != 0 && b[1] != 1) {
			t.Errorf("parseBefore(%q) gave src %d", s, b[1])
		}
	})
}

func FuzzParseAgentFilters(f *testing.F) {
	for _, s := range []string{"live,blocked", "coder", "G-1,G-٣", "claude, cursor", ",,", " live\x0b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fl := parseAgentFilters(map[string]string{"status": s, "role": s, "goal": s, "runtime": s}, []string{"live", "queued"})
		for _, g := range fl.Goal {
			if !agentGoalRe.MatchString(g) {
				t.Errorf("goal %q does not match", g)
			}
		}
		agentsHref(fl, "claude", nil)
	})
}

func FuzzRealpath(f *testing.F) {
	for _, s := range []string{"/", "/a/b/../c", "a//b/./c", "/nonexistent/x", "", "..", "/proc/self/cwd/x", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { pyRealpath(s) })
}

// FuzzPart2Queries renders every part-2 page with arbitrary query values: no panic, no error, no script element.
func FuzzPart2Queries(f *testing.F) {
	for _, pc := range part2Cases() {
		for _, kv := range strings.Split(strings.SplitN(pc.url+"?", "?", 2)[1], "&") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				f.Add(uint8(len(pc.name)), k, v)
			}
		}
	}
	f.Add(uint8(1), "org", "<script>alert(1)</script>")
	c := newPart2DB(f)
	old := ProcSnapshot
	ProcSnapshot = func() map[int64]ProcInfo { return part2Procs() }
	f.Cleanup(func() { ProcSnapshot = old })
	routes := []string{"/agents", "/resources", "/throughput", "/quality", "/changelog", "/tokens"}
	f.Fuzz(func(t *testing.T, which uint8, k, v string) {
		path := routes[int(which)%len(routes)]
		ctx := newTestCtx(c, map[string]string{k: v, "days": v, "org": v})
		view, params, _, ok := Match(path)
		if !ok {
			t.Fatalf("no route %s", path)
		}
		_, body, err := view.Render(ctx, params)
		if err != nil {
			t.Fatalf("%s ?%s=%q: %v", path, k, v, err)
		}
		if strings.Contains(string(body), "<script") {
			t.Fatalf("%s ?%s=%q: a script element in the body", path, k, v)
		}
	})
}
