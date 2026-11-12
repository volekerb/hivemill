INSERT INTO agents(name, role, goal_id, team_id, task_id, status, pane_id, model, started, spawn_args, flagless)
VALUES
 ('claude-coder-t-9', 'coder', 'G-9', NULL, 'T-9', 'live', 'p-9', 'claude-sonnet-5-5', '2026-11-01T10:00:00+01:00', '{"runtime":"claude","org":"claude"}', 0),
 ('cursor-pm-g-9', 'pm', 'G-9', 'TM-1', NULL, 'live', 'p-10', NULL, '2026-11-01T09:00:00+01:00', '{"runtime":"cursor"}', 1),
 ('claude-qa-t-3', 'qa', 'G-3', NULL, 'T-3', 'done', NULL, 'claude-opus-5-5', '2026-10-30T08:00:00+01:00', NULL, 0);
UPDATE agents SET relaunch_requested_at = '2026-11-02T00:00:00+01:00' WHERE name = 'cursor-pm-g-9';
UPDATE config SET value = '9' WHERE key = 'max_live_agents';
