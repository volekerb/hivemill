package s2restier

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_res_org.py (5 cases; see ported_s2cli_restier.txt).

const zeroOrg = "live 0 · workers 0 · leads 0 · hq 0 · queued 0"

// orgLineRE uses \p{Nd} for digit fields: org lines only use ASCII digits in practice; the pattern matches Unicode decimal digits too (G8).
var orgLineRE = regexp.MustCompile(
	`^org (?P<org>claude|cursor): live (?P<live>\p{Nd}+) · workers (?P<workers>\p{Nd}+) · ` +
		`leads (?P<leads>\p{Nd}+) · hq (?P<hq>\p{Nd}+) · queued (?P<queued>\p{Nd}+)$`,
)

type orgFields struct {
	org                              string
	live, workers, leads, hq, queued int
}

func parseOrgLine(t *testing.T, line string) orgFields {
	t.Helper()
	m := orgLineRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("line %q does not match org line pattern", line)
	}
	parse := func(name string) int {
		n, err := strconv.Atoi(m[orgLineRE.SubexpIndex(name)])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return n
	}
	return orgFields{
		org:     m[orgLineRE.SubexpIndex("org")],
		live:    parse("live"),
		workers: parse("workers"),
		leads:   parse("leads"),
		hq:      parse("hq"),
		queued:  parse("queued"),
	}
}

func orgLines(t *testing.T, text string) (claude, cursor string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	var cLine, uLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "org claude: ") {
			cLine = l
		}
		if strings.HasPrefix(l, "org cursor: ") {
			uLine = l
		}
	}
	if cLine == "" || uLine == "" {
		t.Fatalf("missing org lines in:\n%s", text)
	}
	idxC, idxU := indexOf(lines, cLine), indexOf(lines, uLine)
	if idxC >= idxU {
		t.Fatalf("org claude must appear before org cursor: %v", lines)
	}
	return cLine, uLine
}

func indexOf(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}

func resText(e *ft.Env) string { return e.OK("res").Stdout }

func TestResOrgNoAgentsBothOrgsZeroClaudeFirst(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	claude, cursor := orgLines(t, resText(e))
	if claude != "org claude: "+zeroOrg {
		t.Fatalf("claude = %q", claude)
	}
	if cursor != "org cursor: "+zeroOrg {
		t.Fatalf("cursor = %q", cursor)
	}
}

func TestResOrgLiveCursorCoder(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "cursor-coder-t-1", Role: "coder", Goal: "G-001"})
	claude, cursor := orgLines(t, resText(e))
	if claude != "org claude: "+zeroOrg {
		t.Fatalf("claude = %q", claude)
	}
	got := parseOrgLine(t, cursor)
	want := orgFields{org: "cursor", live: 1, workers: 1}
	if got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestResOrgLiveCursorBoss(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "cursor-boss", Role: "boss"})
	_, cursor := orgLines(t, resText(e))
	got := parseOrgLine(t, cursor)
	want := orgFields{org: "cursor", live: 1, hq: 1}
	if got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestResOrgQueuedCursorLead(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "cursor-lead-g-001-a", Role: "lead", Goal: "G-001", Status: "queued"})
	_, cursor := orgLines(t, resText(e))
	got := parseOrgLine(t, cursor)
	want := orgFields{org: "cursor", queued: 1}
	if got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestResOrgClaudeLeadAndCursorCoderSeparateLines(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "claude-lead-g-001-a", Role: "lead", Goal: "G-001"})
	e.SeedAgent(ft.AgentRow{Name: "cursor-coder-t-1", Role: "coder", Goal: "G-001"})
	claude, cursor := orgLines(t, resText(e))
	gotC := parseOrgLine(t, claude)
	wantC := orgFields{org: "claude", live: 1, leads: 1}
	if gotC != wantC {
		t.Fatalf("claude = %+v, want %+v", gotC, wantC)
	}
	gotU := parseOrgLine(t, cursor)
	wantU := orgFields{org: "cursor", live: 1, workers: 1}
	if gotU != wantU {
		t.Fatalf("cursor = %+v, want %+v", gotU, wantU)
	}
	if strings.Contains(claude, "cursor") {
		t.Fatalf("claude line mentions cursor: %q", claude)
	}
	if strings.Contains(strings.TrimPrefix(cursor, "org cursor:"), "claude") {
		t.Fatalf("cursor line mentions claude: %q", cursor)
	}
}

func FuzzParseOrgLine(f *testing.F) {
	f.Add("org claude: live 0 · workers 0 · leads 0 · hq 0 · queued 0")
	f.Add("org cursor: live 1 · workers 1 · leads 0 · hq 0 · queued 0")
	f.Fuzz(func(t *testing.T, line string) {
		m := orgLineRE.FindStringSubmatch(line)
		if m == nil {
			return
		}
		for _, name := range []string{"live", "workers", "leads", "hq", "queued"} {
			// \p{Nd} matches non-ASCII and arbitrarily long digit runs (Python \d parity);
			// Atoi rejects those, which is not a parser failure, so skip.
			if _, err := strconv.Atoi(m[orgLineRE.SubexpIndex(name)]); err != nil {
				t.Skipf("%s not a plain int: %q", name, m[orgLineRE.SubexpIndex(name)])
			}
		}
	})
}
