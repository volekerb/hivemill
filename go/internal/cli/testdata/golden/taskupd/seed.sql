-- Python migrate() adds this column on first connect; adding it here keeps the migration out of every db.diff.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id, name, repo_path, created) VALUES ('P-1', 'p1', '/', '2026-11-01T10:00:00+01:00');
INSERT INTO goals(id, project_id, title, status, success_criteria, created, updated) VALUES
 ('G-1', 'P-1', 'goal one', 'active', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-2', 'P-1', 'goal two (proposed)', 'proposed', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-3', 'P-1', 'goal three', 'active', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-4', 'P-1', 'goal four (planning)', 'planning', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('G-9', 'P-1', 'goal nine', 'done', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO teams(id, goal_id, created) VALUES
 ('TM-1', 'G-1', '2026-11-01T10:00:00+01:00'), ('TM-3', 'G-3', '2026-11-01T10:00:00+01:00');
INSERT INTO agents(name, role, goal_id, status, task_id) VALUES
 ('claude-pm', 'pm', 'G-1', 'live', NULL), ('claude-pm2', 'pm', 'G-2', 'live', NULL),
 ('claude-lead', 'lead', 'G-1', 'live', NULL), ('claude-lead3', 'lead', 'G-3', 'live', NULL),
 ('claude-lead0', 'lead', NULL, 'live', NULL),
 ('claude-coder', 'coder', 'G-1', 'live', NULL), ('claude-reviewer', 'reviewer', 'G-1', 'live', NULL),
 ('claude-qa', 'qa', 'G-1', 'live', NULL), ('claude-judge', 'judge', 'G-1', 'live', NULL),
 ('claude-cleaner', 'cleaner', 'G-1', 'live', NULL), ('claude-director', 'director', NULL, 'live', NULL),
 ('claude-boss', 'boss', NULL, 'live', NULL),
 ('claude-coder-t-0010', 'coder', 'G-1', 'live', 'T-0010'),
 ('claude-coder-t-0013', 'coder', 'G-1', 'queued', 'T-0013'),
 ('claude-obs', 'boss', 'G-1', 'live', 'T-0016');
INSERT INTO tasks(id, goal_id, title, spec, role, status, pr_url, depends_on, waits, sandbox_required, judge_hold_reason, created, updated) VALUES
 ('T-0001', 'G-1', 'todo head', 'plain spec', 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0002', 'G-1', 'pm task', NULL, 'pm', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0003', 'G-1', 'in progress', NULL, 'coder', 'in_progress', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0004', 'G-1', 'in review', NULL, 'coder', 'in_review', 'https://github.com/x/y/pull/4', '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0005', 'G-1', 'blocked owner', NULL, 'coder', 'blocked', NULL, '[]', 'owner', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0006', 'G-1', 'blocked internal', NULL, 'coder', 'blocked', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0007', 'G-1', 'depends on T-0001', NULL, 'coder', 'todo', NULL, '["T-0001"]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0008', 'G-1', 'free todo', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0009', 'G-1', 'done no pr', NULL, 'coder', 'done', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0010', 'G-1', 'held by live worker', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0011', 'G-1', 'judge hold set', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, 'hold me', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0013', 'G-1', 'held by queued worker', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0014', 'G-1', 'sandbox required coder', NULL, 'coder', 'todo', NULL, '[]', 'internal', 1, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0015', 'G-1', 'todo with a PR', NULL, 'coder', 'todo', 'https://github.com/x/y/pull/15', '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0016', 'G-1', 'live non-worker agent', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0017', 'G-1', 'reviewer task', NULL, 'reviewer', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0020', 'G-2', 'task in proposed goal', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0021', 'G-4', 'task in planning goal', NULL, 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0030', 'G-3', 'other goal task naming a.go', 'edit go/internal/cli/a.go', 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0031', 'G-1', 'same goal task naming b.go', 'edit go/internal/cli/b.go', 'coder', 'todo', NULL, '[]', 'internal', 0, NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
