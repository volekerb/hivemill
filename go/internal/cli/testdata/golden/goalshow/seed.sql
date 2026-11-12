INSERT INTO goals(id, title, status, priority, intent, success_criteria, created, updated, org, token_limit) VALUES
 ('G-1','Full goal','active',1,'Intent line one
Intent line two','SC1 first
SC2 second','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',5000),
 ('G-2','Bare goal','proposed',2,NULL,'','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','cursor',0),
 ('G-3','Audit only','planning',2,'only intent',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',7000),
 ('G-4','Null reason','active',3,'x','y','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL),
 ('G-5','Odd line breaks','active',2,'i','s','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00','claude',NULL);
UPDATE goals SET project_id='factory' WHERE id='G-1';
INSERT INTO goal_token_tally(goal_id, input, output, cache_read, cache_write_5m, cache_write_1h, cost, computed_at) VALUES
 ('G-1', 100, 200, 999999, 30, 4, 1.5, '2026-11-01T00:00:00+00:00');
INSERT INTO plans(goal_id, version, body, author, created) VALUES
 ('G-1',1,'old plan','claude-director','2026-11-01T01:00:00+01:00'),
 ('G-1',2,'plan v2 line 1
plan v2 line 2

line 4 after blank
','claude-director','2026-11-01T02:00:00+01:00'),
 ('G-4',1,'p','claude-director','2026-11-01T01:00:00+01:00'),
 ('G-5',1,'a'||char(13)||char(10)||'b'||char(11)||'c'||char(12)||'d'||char(28)||'e'||char(133)||'f'||char(8232)||'g'||char(13)||'h'||char(10),'claude-director','2026-11-01T01:00:00+01:00');
INSERT INTO audits(goal_id, version, verdict, body, author, task_id, created) VALUES
 ('G-1',1,'gaps','first audit','claude-auditor',NULL,'2026-11-01T03:00:00+01:00'),
 ('G-1',2,'ready','audit body
second line','claude-auditor',NULL,'2026-11-01T04:00:00+01:00'),
 ('G-3',1,'gaps','gaps found','claude-auditor',NULL,'2026-11-01T03:00:00+01:00'),
 ('G-4',1,'ready','fine','claude-auditor',NULL,'2026-11-01T03:00:00+01:00');
INSERT INTO signoffs(goal_id, audit_id, snapshot, reason, author, created) VALUES
 ('G-1',1,'{}','stale signoff','owner','2026-11-01T03:30:00+01:00'),
 ('G-1',2,'{}','shipped it','owner','2026-11-01T05:00:00+01:00'),
 ('G-4',4,'{}',NULL,'owner','2026-11-01T06:00:00+01:00');
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created) VALUES
 ('TM-1','G-1','scope one','claude-lead-1','active','2026-11-01T00:00:00+01:00'),
 ('TM-2','G-1',NULL,NULL,'parked','2026-11-01T00:00:00+01:00');
INSERT INTO tasks(id, goal_id, title, role, status, assignee, depends_on, pr_url, created, updated) VALUES
 ('T-1','G-1','Done one','coder','done','claude-coder-t-1','[]','https://example.test/pr/1','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-2','G-1','Todo one','pm','todo',NULL,'["T-1"]',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-3','G-1','Todo two','coder','todo',NULL,'[]',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-4','G-1','Blocked one','coder','blocked',NULL,'[]',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-5','G-1','Weird status','coder','parked',NULL,'[]',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00'),
 ('T-6','G-1','A title that is much longer than sixty characters so the table cell is cut at the limit','coder','cancelled',NULL,'[]',NULL,'2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, pane_id, model, started, spawn_args, flagless) VALUES
 ('claude-coder-t-9','coder','G-1',NULL,NULL,'live','p1','claude-sonnet-5-5','2026-11-01T10:00:00+01:00','{"runtime":"claude"}',0),
 ('claude-director','director','G-1',NULL,NULL,'live','p2','claude-opus-5-5','2026-11-01T09:00:00+01:00','{"runtime":"claude"}',1);
