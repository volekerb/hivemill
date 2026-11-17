package views

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// part2Case is one golden page of the part-2 views: a request URL and the fixture variant (opts, comma separated):
// notready (token index warming up), nousage, unreadable, stale (usage file), nomem, noproc (/proc unreadable).
type part2Case struct{ name, url, opts string }

// part2Cases mirror cases.tsv of the generator (see the PR body); the golden of each is testdata/golden/<name>.txt.
func part2Cases() []part2Case {
	return []part2Case{
		{"agents", "/agents", ""}, {"agents_org_cursor", "/agents?org=cursor", ""}, {"agents_org_claude", "/agents?org=claude", ""},
		{"agents_filter_live_blocked", "/agents?status=live,blocked", ""}, {"agents_role_goal", "/agents?role=coder&goal=G-001", ""},
		{"agents_runtime", "/agents?runtime=claude&org=cursor", ""}, {"agents_ended_stopped", "/agents?status=exited%2Cstopped", ""},
		{"agents_invalid", "/agents?status=bogus&role=nope&goal=G-1x", ""}, {"agents_none", "/agents?status=cancelled", ""},
		{"resources", "/resources", ""}, {"resources_org_cursor", "/resources?org=cursor", ""}, {"resources_org_claude", "/resources?org=claude", ""},
		{"resources_page2", "/resources?before=1794568920-1-73", ""}, {"resources_bad_before", "/resources?before=12-3-4", ""},
		{"resources_nousage", "/resources", "nousage"}, {"resources_unreadable", "/resources", "unreadable"}, {"resources_stale", "/resources", "stale"},
		{"resources_nomem_noproc", "/resources", "nomem,noproc"},
		{"throughput", "/throughput", ""}, {"throughput_7", "/throughput?days=7", ""}, {"throughput_30_sort", "/throughput?days=30&sort=task&dir=asc", ""},
		{"throughput_org_cursor", "/throughput?org=cursor", ""}, {"throughput_org_claude", "/throughput?org=claude&sort=merged&dir=desc", ""},
		{"throughput_notready", "/throughput", "notready"}, {"throughput_bad_days", "/throughput?days=abc", ""},
		{"quality", "/quality", ""}, {"quality_sort_rate", "/quality?sort=rate&dir=asc", ""}, {"quality_org_cursor", "/quality?org=cursor", ""},
		{"quality_org_claude", "/quality?org=claude", ""}, {"quality_sort_team", "/quality?sort=team&dir=desc", ""},
		{"changelog", "/changelog", ""}, {"changelog_by_goal", "/changelog?group=goal", ""}, {"changelog_g001", "/changelog?goal=G-001", ""},
		{"changelog_g002_by_goal", "/changelog?goal=G-002&group=goal", ""}, {"changelog_org_cursor", "/changelog?org=cursor", ""},
		{"changelog_bad_goal", "/changelog?goal=G-999", ""},
		{"tokens", "/tokens", ""}, {"tokens_1", "/tokens?days=1", ""}, {"tokens_30", "/tokens?days=30", ""}, {"tokens_all", "/tokens?days=all", ""},
		{"tokens_task", "/tokens?by=task&days=all", ""}, {"tokens_agent", "/tokens?by=agent&days=all", ""}, {"tokens_model", "/tokens?by=model&days=all", ""},
		{"tokens_org", "/tokens?by=org&days=all", ""}, {"tokens_goal", "/tokens?goal=G-001&days=all", ""},
		{"tokens_model_filter", "/tokens?model=claude-opus-5-5&days=all", ""}, {"tokens_org_cursor", "/tokens?org=cursor&days=all", ""},
		{"tokens_unattributed", "/tokens?goal=unattributed&days=all", ""}, {"tokens_notready", "/tokens", "notready"},
		{"tokens_nousage", "/tokens", "nousage"}, {"tokens_unreadable", "/tokens", "unreadable"}, {"tokens_stale", "/tokens", "stale"},
	}
}

// part2Procs is the /proc snapshot of the goldens: two top-level claude processes and one child of the first.
func part2Procs() map[int64]ProcInfo {
	one, p101 := int64(1), int64(101)
	return map[int64]ProcInfo{101: {PPID: &one}, 102: {PPID: &one}, 103: {PPID: &p101}}
}

// newPart2DB is the data layer fixture plus the part-2 rows.
func newPart2DB(t testing.TB) *data.Conn {
	t.Helper()
	return newTestDB(t, filepath.Join("testdata", "part2.sql"))
}

func hasOpt(opts, o string) bool {
	for _, x := range strings.Split(opts, ",") {
		if x == o {
			return true
		}
	}
	return false
}

// renderPart2 renders a request like web/server.py _view with the fixture variant opts.
func renderPart2(t testing.TB, c *data.Conn, rawURL, opts string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := map[string]string{}
	for k, v := range u.Query() {
		query[k] = v[0]
	}
	old := ProcSnapshot
	t.Cleanup(func() { ProcSnapshot = old })
	ProcSnapshot = func() map[int64]ProcInfo { return part2Procs() }
	if hasOpt(opts, "noproc") {
		ProcSnapshot = func() map[int64]ProcInfo { return nil }
	}
	ctx := newTestCtx(c, query)
	var usage *data.Usage
	switch {
	case hasOpt(opts, "nousage"):
	case hasOpt(opts, "unreadable"):
		usage = &data.Usage{Unreadable: true}
	case hasOpt(opts, "stale"):
		usage = &data.Usage{FiveHour: fp(50), SevenDay: fp(10), Fresh: false, AgeMin: 45}
	default:
		usage = &data.Usage{FiveHour: fp(50), SevenDay: fp(10), Fresh: true, AgeMin: 1}
	}
	ctx.Usage = func() *data.Usage { return usage }
	mem := 4096.0
	var memP *float64
	if !hasOpt(opts, "nomem") {
		memP = &mem
	}
	ctx.Mem = func() *float64 { return memP }
	if hasOpt(opts, "notready") {
		ctx.Tokens = func() *tokens.Snapshot {
			return &tokens.Snapshot{Ready: false, Files: 3, BytesTotal: 3*1048576 + 52429}
		}
	}
	if hasOpt(opts, "hostiletok") { // a session of a hostile agent on a hostile model
		ctx.Tokens = func() *tokens.Snapshot {
			sn := testSnap()
			sn.Sessions["s-x"] = &tokens.Session{FirstTS: sp("2026-11-12T09:00:00+01:00"), LastTS: sp("2026-11-12T10:00:00+01:00"),
				AgentName: sp(xssScript), Project: "p", Cwd: sp("/w/worktrees/G-066-T-66")}
			sn.Rows = append(sn.Rows, tokens.Row{Session: "s-x", Model: xssScript, Day: "2026-11-12", Input: 7, Output: 9, Requests: 1})
			return sn
		}
	}
	v, params, active, ok := Match(u.Path)
	if !ok {
		t.Fatalf("no route for %s", u.Path)
	}
	title, body, err := v.Render(ctx, params)
	if err != nil {
		t.Fatalf("render %s: %v", rawURL, err)
	}
	sh, err := c.ShellStatus(usage, testDefaults, memP, testNow, func(cc *data.Conn, _ time.Time) (any, error) { return tokens.Display(cc.DB, 0) })
	if err != nil {
		t.Fatal(err)
	}
	return Page(title, body, PageOpts{Active: active, Shell: &ShellView{Shell: *sh, Now: testNow}, Org: data.OrgParam(query), Path: u.Path})
}

// TestZZDump (skipped unless DUMP_DB is set) writes the part-2 database and cases.tsv for the golden generator.
func TestZZDump(t *testing.T) {
	out := os.Getenv("DUMP_DB")
	if out == "" {
		t.Skip("set DUMP_DB to dump the part-2 fixture for the golden generator")
	}
	c := newPart2DB(t)
	for _, suf := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(c.Path + suf)
		if err != nil {
			continue
		}
		if err := os.WriteFile(out+suf, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var sb strings.Builder
	for _, pc := range part2Cases() {
		sb.WriteString(pc.name + "\t" + pc.url + "\t" + pc.opts + "\n")
	}
	if err := os.WriteFile(os.Getenv("DUMP_CASES"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runPart2Golden(t *testing.T) {
	c := newPart2DB(t)
	for _, pc := range part2Cases() {
		pc := pc
		t.Run(pc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "golden", pc.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			_, want, ok := strings.Cut(string(raw), "\n") // drop the "# url" header line
			if !ok {
				t.Fatalf("golden %s has no header line", pc.name)
			}
			text, links := NormalizeDOM(renderPart2(t, c, pc.url, pc.opts))
			if got := goldenText(text, links); got != want {
				t.Errorf("%s differs from the Python page\n%s", pc.name, firstDiff(got, want))
			}
		})
	}
}
