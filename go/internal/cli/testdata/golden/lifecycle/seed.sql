-- Python's db() adds this column on first open (bin/factory_cli.py); base.sql lacks it, so the seed adds it and neither side migrates.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id, name, created, gh_repo) VALUES ('P-1','Project one','2026-11-01T00:00:00+01:00','old/name');
INSERT INTO goals(id, project_id, title, intent, success_criteria, status, priority, requested_by, created, updated, org) VALUES
 ('G-1','P-1','Closable goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-2','P-1','Done goal','i','sc','done',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-3','P-1','Open tasks goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-4','P-1','No audit goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-5','P-1','Gaps goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-6','P-1','Proposed goal','i','sc','proposed',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-7','P-1','Changed goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-8','P-1','Org move goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude'),
 ('G-9','P-1','Unsigned goal','i','sc','active',2,'owner','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude');
INSERT INTO tasks(id, goal_id, title, role, status, created, updated) VALUES
 ('T-1','G-1','Done one','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-2','G-1','Audit task','pm','in_progress','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-3','G-3','Todo three','coder','todo','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-4','G-4','Done four','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-5','G-5','Done five','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-7','G-7','Cancelled later','coder','cancelled','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-8','G-7','New after signoff','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-9','G-9','Done nine','coder','done','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00');
INSERT INTO agents(name, role, goal_id, task_id, status, started, flagless) VALUES
 ('claude-boss','boss',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-director','director',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('cursor-director','director',NULL,NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-lead-1','lead','G-1',NULL,'live','2026-11-01T09:00:00+01:00',1),
 ('claude-coder-8','coder','G-8','T-3','live','2026-11-01T09:00:00+01:00',0),
 ('claude-coder-1','coder','G-1',NULL,'live','2026-11-01T09:00:00+01:00',0);
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created, parked_at) VALUES
 ('g-1-a','G-1','scope a','claude-lead-1','active','2026-11-01T00:00:00+01:00',NULL),
 ('g-8-a','G-8','closed team',NULL,'closed','2026-11-01T00:00:00+01:00',NULL),
 ('g-2-a','G-2','team of done goal',NULL,'closed','2026-11-01T00:00:00+01:00',NULL),
 ('g-3-a','G-3','active team',NULL,'active','2026-11-01T00:00:00+01:00',NULL);
INSERT INTO audits(id, goal_id, version, verdict, body, author, task_id, created) VALUES
 (1,'G-1',1,'ready','ok','claude-director','T-2','2026-11-01T04:00:00+01:00'),
 (2,'G-5',1,'gaps','gaps','claude-director',NULL,'2026-11-01T04:00:00+01:00'),
 (3,'G-7',1,'ready','ok','claude-director',NULL,'2026-11-01T04:00:00+01:00'),
 (4,'G-9',1,'ready','ok','claude-director',NULL,'2026-11-01T04:00:00+01:00');
INSERT INTO signoffs(id, goal_id, audit_id, snapshot, reason, author, created) VALUES
 (1,'G-1',1,'{"T-1": "done", "T-2": "in_progress"}','fine','claude-director','2026-11-01T05:00:00+01:00'),
 (2,'G-7',3,'{"T-7": "done"}','fine','claude-director','2026-11-01T05:00:00+01:00');
