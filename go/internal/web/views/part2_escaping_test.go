package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostilePart2SQL adds what the part-2 pages print: an agent whose name, role, summary and task title are hostile, a
// team with a hostile lead, merged tasks with hostile titles, PR URLs and shas (changelog, throughput), a config row
// and a queued reason with markup, a decision message and a goal-less merged task.
const hostilePart2SQL = `
INSERT INTO goals(id, title, intent, success_criteria, status, priority, created, updated, org) VALUES
 ('G-066', '<script>alert(1)</script>', '" onmouseover=x', '1. x', 'active', 1, '2026-11-10T09:00:00+01:00', '2026-11-14T09:00:00+01:00', 'claude');
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created) VALUES
 ('tm-x', 'G-066', '" onmouseover=x <script>alert(1)</script>', '"><script>alert(1)</script>', 'active', '2026-11-10T09:00:00+01:00');
INSERT INTO tasks(id, goal_id, team_id, title, role, status, assignee, depends_on, pr_url, attempts, created, updated, follow_up_of, waits) VALUES
 ('T-66', 'G-066', 'tm-x', '<script>alert(1)</script> [SC1]', 'coder', 'done', '" onmouseover=x', '[]', 'https://github.com/o/r/pull/66', 2, '2026-11-13T09:00:00+01:00', '2026-11-14T09:00:00+01:00', NULL, 'internal'),
 ('T-67', 'G-066', 'tm-x', '" onmouseover=x', 'coder', 'in_review', '<script>alert(1)</script>', '[]', 'javascript:alert(1)', 2, '2026-11-13T09:00:00+01:00', '2026-11-14T09:00:00+01:00', NULL, 'internal');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started, ended, summary, blocked_since, org) VALUES
 ('<script>alert(1)</script>', '" onmouseover=x', 'G-066', 'tm-x', 'T-67', 'live', '2026-11-14T08:00:00+01:00', NULL, NULL, 1794650400.0, 'claude'),
 ('" onmouseover=x', 'coder', 'G-066', 'tm-x', 'T-66', 'done', '2026-11-14T08:00:00+01:00', '2026-11-14T10:00:00+01:00', '<script>alert(1)</script> " onmouseover=x', NULL, 'claude'),
 ('claude-queued-x', 'coder', 'G-066', 'tm-x', 'T-67', 'queued', NULL, NULL, NULL, NULL, 'claude');
UPDATE agents SET queued_at = '2026-11-14T11:30:00+01:00' WHERE name = 'claude-queued-x';
INSERT INTO config(key, value) VALUES ('<script>alert(1)</script>', '" onmouseover=x <script>alert(1)</script>');
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-13T09:00:00+01:00', 'claude-coder-t-66', 'task', 'T-66', 'update', 'status=in_review pr=https://github.com/o/r/pull/66'),
 ('2026-11-14T10:00:00+01:00', 'claude-judge', 'task', 'T-66', 'merged', '@<script>alert(1)</script>'),
 ('2026-11-14T11:30:00+01:00', 'factory', 'agent', 'claude-queued-x', 'queued', '<script>alert(1)</script> " onmouseover=x'),
 ('2026-11-14T11:31:00+01:00', 'owner', 'config', '" onmouseover=x', 'set', '<script>alert(1)</script>');
INSERT INTO verdicts(task_id, pr_url, head_sha, role, verdict, summary, author, created) VALUES
 ('T-66', 'https://github.com/o/r/pull/66', 'abc', 'reviewer', 'fail', '<script>alert(1)</script>', '" onmouseover=x', '2026-11-13T10:00:00+01:00'),
 ('T-66', 'https://github.com/o/r/pull/66', 'abc', 'judge', 'merge', 'm', 'claude-judge', '2026-11-14T10:00:00+01:00'),
 ('T-67', 'javascript:alert(1)', 'abd', 'reviewer', 'fail', 'x', 'y', '2026-11-14T10:10:00+01:00');
INSERT INTO messages(sender, recipient, kind, subject, body, goal_id, task_id, created, read_at) VALUES
 ('<script>alert(1)</script>', '" onmouseover=x', 'decision', '<script>alert(1)</script> " onmouseover=x', 'b', 'G-066', 'T-66', '2026-11-14T11:00:00+01:00', NULL);
`

// TestEscapingPart2: hostile agent names, roles, summaries, scopes, lead names, task and goal titles, PR URLs, shas,
// config keys and values, event details, message subjects and token models render escaped in every part-2 view, and
// hostile query values (?org=, ?goal=, ?model=, ?sort=, ?before=, ?status=) never reach the markup.
func TestEscapingPart2(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "hostile2.sql")
	if err := os.WriteFile(extra, []byte(hostilePart2SQL), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestDB(t, filepath.Join("testdata", "part2.sql"), extra)
	script, attr := `&lt;script&gt;alert(1)&lt;/script&gt;`, `&#34; onmouseover=x`
	type page struct {
		url, opts string
		want      []string // escaped forms that must appear (proves the hostile row was rendered, not dropped)
	}
	pages := []page{
		{"/agents", "", []string{script, attr}},
		{"/agents?status=live,blocked", "", []string{script}},
		{"/agents?role=coder&goal=G-066", "", []string{attr}},
		{"/agents?status=" + xssScript + "&role=" + xssAttr + "&goal=" + xssScript + "&runtime=" + xssAttr + "&org=" + xssAttr, "", nil},
		{"/resources", "", []string{script, attr}},
		{"/resources?before=" + xssScript + "&org=" + xssAttr, "", nil},
		{"/throughput?days=30", "", []string{script}},
		{"/throughput?days=30&sort=" + xssScript + "&dir=" + xssAttr + "&org=" + xssAttr, "", nil},
		{"/quality", "", []string{attr}},
		{"/quality?sort=" + xssScript + "&dir=" + xssAttr + "&org=" + xssAttr, "", nil},
		{"/changelog", "", []string{script}},
		{"/changelog?group=goal", "", []string{script}},
		{"/changelog?goal=G-066", "", []string{script}},
		{"/changelog?goal=" + xssScript + "&group=" + xssAttr + "&org=" + xssAttr, "", nil},
		{"/tokens?days=all", "hostiletok", []string{script}},
		{"/tokens?days=all&by=agent", "hostiletok", []string{script}},
		{"/tokens?days=all&by=model", "hostiletok", []string{script}},
		{"/tokens?days=all&by=goal", "hostiletok", []string{script}},
		{"/tokens?days=all&by=task", "hostiletok", []string{script}},
		{"/tokens?days=" + xssScript + "&by=" + xssAttr + "&goal=" + xssScript + "&model=" + xssAttr + "&org=" + xssAttr, "hostiletok", nil},
		{"/tokens?model=" + xssScript + "&days=all", "hostiletok", []string{script}},
	}
	for _, p := range pages {
		p := p
		t.Run(p.url, func(t *testing.T) {
			doc := renderPart2(t, c, p.url, p.opts)
			for _, problem := range escapingProblems(doc) {
				t.Errorf("%s: %s", p.url, problem)
			}
			for _, w := range p.want {
				if !strings.Contains(doc, w) {
					t.Errorf("%s: escaped form %q not found", p.url, w)
				}
			}
		})
	}
}
