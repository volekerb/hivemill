INSERT OR REPLACE INTO config(key, value) VALUES ('orgs_enabled', 'claude,cursor');
INSERT INTO goals(id, project_id, title, intent, status, priority, created, updated, org) VALUES
 ('G-1', NULL, 'Active goal', 'i', 'active', 1, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'claude'),
 ('G-7', NULL, 'Cursor goal', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'cursor');
INSERT INTO agents(name, role, status, goal_id, team_id, task_id, started, ended, flagless, flagless_since) VALUES
 ('claude-lead-a', 'lead', 'live', 'G-1', NULL, NULL, '2026-11-01T00:00:00+00:00', NULL, 0, NULL),
 ('claude-coder-14', 'coder', 'live', 'G-1', NULL, NULL, '2026-11-12T10:00:00+00:00', NULL, 0, NULL),
 ('claude-boss', 'boss', 'live', NULL, NULL, NULL, '2026-11-01T00:00:00+00:00', NULL, 0, NULL),
 ('claude-director', 'director', 'live', NULL, NULL, NULL, '2026-11-01T00:00:00+00:00', NULL, 0, NULL);
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-12T07:00:00+00:00', 'claude-judge', 'task', 'T-11', 'merged', '@abc1234');
