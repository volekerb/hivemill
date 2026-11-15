-- Rows for the done/prcheck/pr/src tests, applied on top of testdata/golden/base.sql.
INSERT INTO projects(id, name, repo_path, gh_repo, base_branch, created) VALUES
 ('P-1', 'p1', '/', 'acme/app', 'main', '2026-11-01T10:00:00+01:00');
INSERT INTO goals(id, project_id, title, status, created, updated) VALUES
 ('G-1', 'P-1', 'goal one', 'active', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO teams(id, goal_id, lead_agent, created) VALUES
 ('TM-1', 'G-1', 'claude-lead', '2026-11-01T10:00:00+01:00'),
 ('TM-2', 'G-1', NULL, '2026-11-01T10:00:00+01:00');
INSERT INTO tasks(id, goal_id, team_id, title, role, status, created, updated) VALUES
 ('T-0001', 'G-1', 'TM-1', 'coder task', 'coder', 'in_progress', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0002', 'G-1', 'TM-1', 'judge task', 'coder', 'in_review', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0010', 'G-1', 'TM-1', 'pm task', 'pm', 'in_progress', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0011', 'G-1', 'TM-1', 'qa task', 'qa', 'in_progress', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0012', 'G-1', 'TM-1', 'forced task', 'pm', 'in_progress', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0013', 'G-1', 'TM-2', 'cancelled task', 'pm', 'cancelled', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0014', 'G-1', 'TM-1', 'qa with verdict', 'qa', 'in_progress', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO verdicts(task_id, role, verdict, created) VALUES ('T-0014', 'qa', 'pass', '2026-11-01T10:00:00+01:00');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started) VALUES
 ('claude-pm', 'pm', 'G-1', 'TM-1', 'T-0010', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-pm-nolead', 'pm', 'G-1', 'TM-2', 'T-0013', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-qa-blocked', 'qa', 'G-1', 'TM-1', 'T-0011', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-qa-verdict', 'qa', 'G-1', 'TM-1', 'T-0014', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-pm-orders', 'pm', 'G-1', 'TM-1', 'T-0012', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-coder', 'coder', 'G-1', 'TM-1', 'T-0001', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-judge', 'judge', 'G-1', 'TM-1', 'T-0002', 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-lead', 'lead', 'G-1', 'TM-1', NULL, 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-director', 'director', NULL, NULL, NULL, 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-boss', 'boss', NULL, NULL, NULL, 'live', '2026-11-12T00:00:00+00:00'),
 ('claude-gone', 'pm', 'G-1', 'TM-1', NULL, 'done', '2026-11-12T00:00:00+00:00');
INSERT INTO messages(sender, recipient, kind, subject, body, goal_id, task_id, created) VALUES
 ('claude-lead', 'claude-pm-orders', 'request', 'do the thing', 'line one'||char(10)||char(10)||'line three', 'G-1', 'T-0012', '2026-11-12T00:10:00+00:00'),
 ('claude-lead', 'claude-pm-orders', 'msg', 'fyi', NULL, NULL, NULL, '2026-11-12T00:11:00+00:00');
