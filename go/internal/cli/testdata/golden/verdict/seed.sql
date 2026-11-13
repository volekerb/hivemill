-- Python migrate() adds this column on first connect; adding it here keeps the migration out of every db.diff.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id, name, repo_path, created) VALUES ('P-1', 'p1', '/', '2026-11-01T10:00:00+01:00');
INSERT INTO goals(id, project_id, title, status, created, updated) VALUES
 ('G-1', 'P-1', 'goal one', 'active', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO agents(name, role, goal_id, status, task_id, started) VALUES
 ('claude-qa', 'qa', 'G-1', 'live', NULL, '2026-11-12T00:00:00+00:00'),
 ('claude-reviewer', 'reviewer', 'G-1', 'live', NULL, '2026-11-12T00:00:00+00:00'),
 ('claude-qa2', 'qa', 'G-1', 'live', 'T-0002', '2026-11-12T00:30:00+00:00'),
 ('claude-qa3', 'qa', 'G-1', 'live', 'T-0003', '2026-11-12T00:30:00+00:00'),
 ('claude-reviewer4', 'reviewer', 'G-1', 'live', 'T-0004', '2026-11-12T00:30:00+00:00'),
 ('claude-combined-t-0001', 'reviewer', 'G-1', 'live', 'T-0001', '2026-11-12T00:30:00+00:00'),
 ('claude-coder', 'coder', 'G-1', 'live', NULL, NULL),
 ('claude-lead', 'lead', 'G-1', 'live', NULL, NULL);
INSERT INTO tasks(id, goal_id, title, role, status, pr_url, created, updated) VALUES
 ('T-0001', 'G-1', 'with PR', 'coder', 'in_review', 'https://github.com/x/y/pull/1', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0002', 'G-1', 'started head moved', 'coder', 'in_review', 'https://github.com/x/y/pull/2', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0003', 'G-1', 'started head is current', 'coder', 'in_review', 'https://github.com/x/y/pull/3', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0004', 'G-1', 'stale autospawn event', 'coder', 'in_review', 'https://github.com/x/y/pull/4', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0005', 'G-1', 'no PR', 'coder', 'in_progress', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-12T00:31:00+00:00', 'tick', 'task', 'T-0002', 'review-autospawn', '{"agent": "claude-qa2", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'),
 ('2026-11-12T00:31:00+00:00', 'tick', 'task', 'T-0003', 'review-autospawn', '{"agent": "claude-qa3", "sha": "0123456789abcdef0123456789abcdef01234567"}'),
 ('2026-11-12T00:10:00+00:00', 'tick', 'task', 'T-0004', 'review-autospawn', '{"agent": "claude-reviewer4", "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}');
INSERT INTO verdicts(task_id, pr_url, head_sha, role, verdict, summary, author, created) VALUES
 ('T-0001', 'https://github.com/x/y/pull/1', '0123456789abcdef0123456789abcdef01234567', 'qa', 'fail', 'earlier', 'claude-qa', '2026-11-11T10:00:00+00:00');
