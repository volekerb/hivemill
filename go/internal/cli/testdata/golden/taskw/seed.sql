-- Python migrate() adds this column on first connect; adding it here keeps the migration out of every db.diff.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id, name, repo_path, created) VALUES ('P-1', 'p1', '/', '2026-11-01T10:00:00+01:00');
INSERT INTO goals(id, project_id, title, status, success_criteria, created, updated) VALUES
 ('G-1', 'P-1', 'goal one', 'active', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-2', 'P-1', 'goal two', 'active', 'SC1 something', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-3', 'P-1', 'goal three', 'active', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-4', NULL, 'goal without project', 'active', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-9', 'P-1', 'goal nine', 'done', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO agents(name, role, goal_id, status) VALUES
 ('claude-pm', 'pm', 'G-1', 'live'), ('claude-pm2', 'pm', 'G-2', 'live'),
 ('claude-lead', 'lead', 'G-1', 'live'), ('claude-coder', 'coder', 'G-1', 'live');
INSERT INTO tasks(id, goal_id, title, spec, status, pr_url, depends_on, created, updated) VALUES
 ('T-0001', 'G-1', 'done no pr', NULL, 'done', NULL, '[]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0002', 'G-1', 'done with pr', NULL, 'done', 'https://github.com/x/y/pull/1', '[]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0003', 'G-1', 'todo naming a.go', 'edit go/internal/cli/a.go please', 'todo', NULL, '[]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0004', 'G-3', 'other goal naming b.go', 'touches go/internal/cli/b.go', 'in_progress', NULL, '[]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0005', 'G-9', 'done in done goal', NULL, 'done', NULL, '[]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0006', 'G-1', 'chain head', NULL, 'todo', NULL, '["T-0007"]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0007', 'G-1', 'chain tail (depends on the next id)', NULL, 'todo', NULL, '["T-0008"]', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
