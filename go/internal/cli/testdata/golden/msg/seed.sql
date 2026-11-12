-- Python's migrate() adds this column on first open; adding it here keeps the DB diff to the command's own writes.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO goals(id, title, status, created, updated, org) VALUES
 ('G-9', 'claude goal', 'active', '2026-11-01T08:00:00+01:00', '2026-11-01T08:00:00+01:00', 'claude'),
 ('G-C', 'cursor goal', 'active', '2026-11-01T08:00:00+01:00', '2026-11-01T08:00:00+01:00', 'cursor');
INSERT INTO teams(id, goal_id, lead_agent, created) VALUES
 ('TM-1', 'G-9', 'lead-a', '2026-11-01T08:00:00+01:00'),
 ('TM-2', 'G-C', 'cursor-lead', '2026-11-01T08:00:00+01:00'),
 ('TM-3', 'G-9', NULL, '2026-11-01T08:00:00+01:00');
INSERT INTO tasks(id, goal_id, team_id, title, created, updated) VALUES
 ('T-9', 'G-9', 'TM-1', 'nine', '2026-11-01T08:00:00+01:00', '2026-11-01T08:00:00+01:00'),
 ('T-C', 'G-C', 'TM-2', 'cursor nine', '2026-11-01T08:00:00+01:00', '2026-11-01T08:00:00+01:00');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started, org) VALUES
 ('boss', 'boss', NULL, NULL, NULL, 'live', '2026-11-01T07:00:00+01:00', NULL),
 ('director', 'director', NULL, NULL, NULL, 'live', '2026-11-01T07:01:00+01:00', NULL),
 ('cursor-boss', 'boss', NULL, NULL, NULL, 'live', '2026-11-01T07:02:00+01:00', 'cursor'),
 ('cursor-director', 'director', NULL, NULL, NULL, 'live', '2026-11-01T07:03:00+01:00', 'cursor'),
 ('lead-a', 'lead', 'G-9', 'TM-1', NULL, 'live', '2026-11-01T09:00:00+01:00', NULL),
 ('cursor-lead', 'lead', 'G-C', 'TM-2', NULL, 'live', '2026-11-01T09:00:00+01:00', 'cursor'),
 ('coder-a', 'coder', 'G-9', 'TM-1', 'T-9', 'live', '2026-11-01T10:00:00+01:00', NULL),
 ('coder-n', 'coder', 'G-9', 'TM-3', NULL, 'live', '2026-11-01T10:10:00+01:00', NULL),
 ('qa-a', 'qa', 'G-9', 'TM-1', NULL, 'live', '2026-11-01T10:30:00+01:00', NULL),
 ('qa-b', 'qa', 'G-9', 'TM-1', NULL, 'live', '2026-11-01T11:30:00+01:00', NULL),
 ('qa-old', 'qa', 'G-9', 'TM-1', NULL, 'exited', '2026-11-01T12:30:00+01:00', NULL);
