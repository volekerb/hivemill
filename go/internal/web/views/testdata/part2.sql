-- Rows the part-2 view goldens (T-1476) need on top of ../../data/testdata/fixture.sql and extra.sql: ended and
-- blocked agents, a parked team, markup in agent names and changelog titles, more merges and rejections.
UPDATE teams SET parked_at = '2026-11-14T09:00:00+01:00' WHERE id = 'tm-c';
UPDATE agents SET blocked_since = 1794650400.0 WHERE name = 'claude-coder-t-2';
INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,pane_id,session_id,spawn_args,queued_at,started,ended,stop_requested_at,org,summary) VALUES
 ('claude-coder-t-20','coder','G-001','tm-a','T-5','done','claude-sonnet-5-5',NULL,'s-20',NULL,NULL,'2026-11-14T09:00:00+01:00','2026-11-14T10:30:00+01:00',NULL,'claude','Opened the PR <b>bold</b> & more'),
 ('claude-qa-t-21','qa','G-001','tm-a','T-2','exited','claude-sonnet-5-5',NULL,'s-21',NULL,NULL,'2026-11-14T10:00:00+01:00','2026-11-14T11:30:00+01:00',NULL,'claude',NULL),
 ('cursor-judge-t-22','judge','G-002','tm-c','T-6','stopped','grok-4.7-high',NULL,NULL,'{"runtime":"cursor"}',NULL,'2026-11-14T08:00:00+01:00','2026-11-14T09:00:00+01:00',NULL,'cursor','stopped by owner'),
 ('claude-<b>x</b>-t-23','coder','G-001','tm-a','T-9','live','claude-sonnet-5-5','p9','s-23',NULL,NULL,'2026-11-14T11:20:00+01:00',NULL,'2026-11-14T11:58:00+01:00','claude',NULL),
 ('cursor-evaluator-t-24','evaluator','G-002',NULL,NULL,'live','claude-sonnet-5-5','p10','s-24','{"runtime":"claude"}',NULL,'2026-11-14T11:40:00+01:00',NULL,NULL,'cursor',NULL),
 ('claude-coder-t-25','coder','G-001','tm-a','T-12','queued',NULL,NULL,NULL,NULL,'2026-11-14T11:50:00+01:00',NULL,NULL,NULL,NULL,NULL);
UPDATE agents SET blocked_since = 'not a number' WHERE name = 'claude-<b>x</b>-t-23';
UPDATE agents SET goal_id = 'G-1' WHERE name = 'claude-qa-t-21';
INSERT INTO tasks(id,goal_id,team_id,title,role,status,assignee,depends_on,pr_url,attempts,created,updated,follow_up_of,waits) VALUES
 ('T-30','G-001','tm-a','Merged <i>markup</i> title [SC1]','coder','done','claude-coder-t-30','[]','https://x/pr/30',2,'2026-11-13T08:00:00+01:00','2026-11-14T10:00:00+01:00',NULL,'internal'),
 ('T-31','G-002','tm-c','Cursor merged work','coder','done','cursor-coder-t-31','[]','https://x/pr/31',1,'2026-11-12T08:00:00+01:00','2026-11-13T10:00:00+01:00',NULL,'internal');
INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES
 ('2026-11-13T08:10:00+01:00','factory','agent','claude-coder-t-30','spawn',NULL),
 ('2026-11-13T09:00:00+01:00','claude-coder-t-30','task','T-30','update','status=in_review pr=https://x/pr/30'),
 ('2026-11-14T10:00:00+01:00','claude-judge','task','T-30','merged','@cafe123'),
 ('2026-11-12T08:10:00+01:00','factory','agent','cursor-coder-t-31','spawn',NULL),
 ('2026-11-13T10:00:00+01:00','cursor-judge','task','T-31','merged','@beef456'),
 ('2026-11-14T10:05:00+01:00','claude-judge','task','T-30','judge-reject','x'),
 ('2026-11-14T10:30:00+01:00','factory','team','tm-a','eval','score 4/5 and a long tail'),
 ('2026-11-14T10:31:00+01:00','owner','config','max_five_hour_pct','set','90 <b>markup</b>'),
 ('2026-11-14T10:32:00+01:00','claude-boss','digest','all','sent','x');
INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES
 ('T-30','https://x/pr/30','3030','reviewer','pass','ok','claude-reviewer-t-30','2026-11-13T09:30:00+01:00'),
 ('T-30','https://x/pr/30','3030','judge','merge','m','claude-judge','2026-11-14T10:00:00+01:00'),
 ('T-31','https://x/pr/31','3131','judge','merge','m','cursor-judge','2026-11-13T10:00:00+01:00');
INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created,read_at) VALUES
 ('claude-director','claude-boss','decision','Decision <b>one</b>','b','G-001','T-30','2026-11-14T10:40:00+01:00',NULL),
 ('claude-lead-g-001','claude-boss','alert','Alert text','b','G-001',NULL,'2026-11-13T10:40:00+01:00',NULL);
INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,pane_id,session_id,spawn_args,queued_at,started,ended,stop_requested_at,org,summary) VALUES
 ('claude-director','director',NULL,NULL,NULL,'live','claude-opus-5-5','p11','s-dir',NULL,NULL,'2026-11-14T08:00:00+01:00',NULL,NULL,'claude',NULL);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 50)
INSERT INTO events(ts,actor,entity,entity_id,action,detail)
SELECT '2026-11-13T12:' || printf('%02d', i % 60) || ':00+01:00', 'factory', 'agent', 'bulk-' || i, 'spawn', NULL FROM n;
