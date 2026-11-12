-- Python's db() adds this column on first open (bin/factory_cli.py:546); base.sql lacks it, so the seed adds it and neither side migrates.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO goals(id, title, status, priority, created, updated, org) VALUES
 ('G-1','Proposed goal','proposed',2,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-2','Active goal','active',2,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude');
INSERT INTO plans(goal_id, version, body, author, created) VALUES ('G-2',1,'old plan','claude-director','2026-11-01T01:00:00+01:00');
INSERT INTO agents(name, role, goal_id, status, started, flagless) VALUES
 ('claude-director','director',NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-lead-1','lead','G-1','live','2026-11-01T09:00:00+01:00',1),
 ('claude-pm-1','pm','G-1','live','2026-11-01T09:00:00+01:00',1),
 ('claude-coder-1','coder','G-1','live','2026-11-01T09:00:00+01:00',0);
