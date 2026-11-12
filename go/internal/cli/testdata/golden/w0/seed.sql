-- Python migrate() adds this column on first connect; adding it here keeps the migration out of every db.diff.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO goals(id, title, status, created, updated) VALUES ('G-1', 'w0 goal', 'active', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO tasks(id, goal_id, title, created, updated) VALUES ('T-0001', 'G-1', 'w0 task', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
