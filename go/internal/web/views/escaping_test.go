package views

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	xssScript = `<script>alert(1)</script>`
	xssAttr   = `" onmouseover=x`
)

// hostileSQL adds a goal, a team, tasks, an agent and an escalation whose free-text fields are hostile: a script
// element in titles, scope, intent, criteria, subject and event details, and an attribute-breaking quote in others.
const hostileSQL = `
INSERT INTO goals(id, title, intent, success_criteria, status, priority, created, updated) VALUES
 ('G-066', '<script>alert(1)</script>', '" onmouseover=x <script>alert(1)</script>', '1. <script>alert(1)</script> [SC1]', 'active', 1,
  '2026-11-10T09:00:00+01:00', '2026-11-14T09:00:00+01:00');
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created) VALUES
 ('tm-x', 'G-066', '" onmouseover=x <script>alert(1)</script>', '"><script>alert(1)</script>', 'active', '2026-11-10T09:00:00+01:00');
INSERT INTO tasks(id, goal_id, team_id, title, role, status, assignee, depends_on, pr_url, attempts, created, updated, follow_up_of, waits) VALUES
 ('T-66', 'G-066', 'tm-x', '<script>alert(1)</script>', 'coder', 'blocked', '" onmouseover=x', '[]', NULL, 1, '2026-11-10T09:00:00+01:00', '2026-11-14T09:00:00+01:00', NULL, 'owner'),
 ('T-67', 'G-066', 'tm-x', '" onmouseover=x', 'coder', 'in_review', '<script>alert(1)</script>', '[]', 'javascript:alert(1)', 2, '2026-11-10T09:00:00+01:00', '2026-11-14T09:00:00+01:00', 'T-66', 'internal'),
 ('T-68', 'G-066', 'tm-x', '<script>alert(1)</script> [SC1]', 'coder', 'in_progress', NULL, '[]', 'https://github.com/o/r/pull/68', 0, '2026-11-10T09:00:00+01:00', '2026-11-14T09:00:00+01:00', NULL, 'internal');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started) VALUES
 ('claude-coder-t-67', 'coder', 'G-066', 'tm-x', 'T-67', 'live', '2026-11-14T08:00:00+01:00');
INSERT INTO messages(sender, recipient, kind, subject, body, goal_id, task_id, created, read_at) VALUES
 ('claude-lead-g-066', 'claude-boss', 'escalation', '<script>alert(1)</script> " onmouseover=x', 'b', 'G-066', 'T-67', '2026-11-14T09:00:00+01:00', NULL);
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-12T09:00:00+01:00', '" onmouseover=x', 'task', 'T-67', 'update', 'status=in_review pr=<script>alert(1)</script>'),
 ('2026-11-12T09:10:00+01:00', '<script>alert(1)</script>', 'task', 'T-67', 'judge-blocked', '<script>alert(1)</script> " onmouseover=x'),
 ('2026-11-13T09:10:00+01:00', 'claude-judge', 'task', 'T-67', 'follow-up', 'see T-66 and <script>alert(1)</script>');
INSERT INTO verdicts(task_id, pr_url, head_sha, role, verdict, summary, author, created) VALUES
 ('T-67', 'javascript:alert(1)', '<script>alert(1)</script>', 'reviewer', 'fail', '<script>alert(1)</script>', '" onmouseover=x', '2026-11-12T09:20:00+01:00');
`

var (
	quotedRe = regexp.MustCompile(`"[^"]*"`)
	onAttrRe = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// escapingProblems lists what is wrong with a page that must hold hostile data only as text: a raw script element, a
// second script element (only theme.js is allowed), an attribute-breaking quote left raw, an event-handler attribute
// name, a javascript: URL.
func escapingProblems(doc string) []string {
	var out []string
	if strings.Contains(doc, "<script>alert") {
		out = append(out, "raw script element")
	}
	if n := strings.Count(doc, "<script"); n != 1 {
		out = append(out, fmt.Sprintf("%d script elements, want exactly the theme.js one", n))
	}
	if strings.Contains(doc, xssAttr) {
		out = append(out, "attribute-breaking quote left raw")
	}
	for _, tag := range anyTagRe.FindAllString(doc, -1) {
		if onAttrRe.MatchString(quotedRe.ReplaceAllString(tag, `""`)) { // attribute values are text; only an attribute NAME is a handler
			out = append(out, "event-handler attribute in tag "+tag)
		}
		if strings.Contains(tag, "javascript:") {
			out = append(out, "javascript: URL in tag "+tag)
		}
	}
	return out
}

var anyTagRe = regexp.MustCompile(`<[^>]*>`)

// TestEscaping: hostile titles, scope, intent, criteria, subjects, actors and event details render escaped in every
// view: no raw script element, no attribute-breaking quote, no on* attribute, and the script-src stays the one theme.js.
func TestEscaping(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "hostile.sql")
	if err := os.WriteFile(extra, []byte(hostileSQL), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestDB(t, extra)
	type page struct {
		path  string
		query map[string]string
		want  []string // escaped forms that must appear (proves the hostile row was rendered, not dropped)
	}
	script, attr := `&lt;script&gt;alert(1)&lt;/script&gt;`, `&#34; onmouseover=x`
	pages := []page{
		{"/", nil, []string{script}},
		{"/", map[string]string{"org": "claude"}, []string{script}},
		{"/goals", nil, []string{script}},
		{"/goal/G-066", nil, []string{script, attr}},
		{"/tasks", nil, []string{script, attr}},
		{"/tasks", map[string]string{"goal": "G-066", "status": "blocked", "sort": xssScript, "dir": xssAttr}, []string{script}},
		{"/tasks", map[string]string{"sort": xssScript, "dir": "asc", "goal": xssAttr, "status": xssScript, "org": xssAttr}, nil},
		{"/goals", map[string]string{"sort": xssScript, "org": xssAttr}, nil},
		{"/task/T-66", nil, []string{script}},
		{"/task/T-67", nil, []string{attr, script}},
		{"/task/T-68", nil, []string{script}},
	}
	for _, p := range pages {
		p := p
		t.Run(p.path+"?"+strings.Join(mapVals(p.query), "&"), func(t *testing.T) {
			doc := renderPage(t, c, p.path, p.query)
			for _, problem := range escapingProblems(doc) {
				t.Errorf("%s: %s", p.path, problem)
			}
			for _, w := range p.want {
				if !strings.Contains(doc, w) {
					t.Errorf("%s: escaped form %q not found", p.path, w)
				}
			}
		})
	}
}

func mapVals(m map[string]string) []string {
	var out []string
	for _, k := range []string{"org", "goal", "status", "sort", "dir"} {
		if v, ok := m[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// TestEscapingRejectsUnescaped is the negative control of TestEscaping: the same hostile data pushed through the
// raw string path (Safe, as a view would if it concatenated) is caught by the checks the test applies.
func TestEscapingRejectsUnescaped(t *testing.T) {
	for name, raw := range map[string]string{
		"raw script":  `<html><script src="/static/theme.js"></script><p><script>alert(1)</script></p></html>`,
		"second tag":  `<script src="/static/theme.js"></script><script src="x"></script>`,
		"raw quote":   `<script src="a"></script><a title="" onmouseover=x">x</a>`,
		"handler":     `<script src="a"></script><a href="/" onclick=alert(1)>x</a>`,
		"js url":      `<script src="a"></script><a href="javascript:alert(1)">x</a>`,
		"no theme.js": `<p>no script at all</p>`,
	} {
		if len(escapingProblems(raw)) == 0 {
			t.Errorf("%s: escapingProblems accepted an unsafe page", name)
		}
	}
	if p := escapingProblems(`<script src="/static/theme.js"></script><a title="&#34; onmouseover=x" href="/x">&lt;script&gt;</a>`); len(p) != 0 {
		t.Errorf("escapingProblems rejected a safe page: %v", p)
	}
	good := string(El("p", xssScript))
	if strings.Contains(good, "<script>alert") || !strings.Contains(good, "&lt;script&gt;") {
		t.Errorf("El did not escape: %s", good)
	}
	if h := string(El("a", At("title", xssAttr), "x")); strings.Contains(h, xssAttr) || !strings.Contains(h, `title="&#34; onmouseover=x"`) {
		t.Errorf("attribute value not escaped: %s", h)
	}
}
