-- Python's db() adds this column on first open (bin/factory_cli.py:546); base.sql lacks it, so the seed adds it and neither side migrates.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id, name, created) VALUES ('P-1','Project one','2026-11-01T00:00:00+01:00');
INSERT INTO goals(id, project_id, title, intent, success_criteria, status, priority, requested_by, created, updated, org, token_limit) VALUES
 ('G-1','P-1','Active goal','intent','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',5000),
 ('G-2','P-1','Done goal','intent','sc','done',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL),
 ('G-3','P-1','Pm goal','intent','sc','planning',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL),
 ('G-4','P-1','Signoff goal','intent','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL),
 ('G-5','P-1','Open tasks goal','intent','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','cursor',NULL),
 ('G-6','P-1','Fresh goal','intent','sc','proposed',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL);
INSERT INTO tasks(id, goal_id, title, role, status, created, updated) VALUES
 ('T-1','G-1','Done one','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-3','G-3','Done three','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-4','G-4','Done four','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-5','G-5','Todo five','coder','todo','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-9','G-3','Pm own task','pm','in_progress','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-14','G-4','Cancelled four','coder','cancelled','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00');
INSERT INTO agents(name, role, goal_id, task_id, status, started, flagless) VALUES
 ('claude-boss','boss',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-director','director',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('cursor-boss','boss',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-lead-1','lead','G-1',NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-pm-3','pm','G-3','T-9','live','2026-11-01T09:00:00+01:00',1),
 ('claude-pm-4','pm','G-4',NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-coder-1','coder','G-1',NULL,'live','2026-11-01T09:00:00+01:00',0);
INSERT INTO audits(goal_id, version, verdict, body, author, task_id, created) VALUES
 ('G-4',1,'gaps','first','claude-pm-4',NULL,'2026-11-01T03:00:00+01:00'),
 ('G-4',2,'ready','second','claude-pm-4',NULL,'2026-11-01T04:00:00+01:00'),
 ('G-1',1,'gaps','gaps only','claude-pm-4',NULL,'2026-11-01T04:00:00+01:00');
