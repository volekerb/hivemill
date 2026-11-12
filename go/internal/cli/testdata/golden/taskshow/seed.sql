ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO goals(id, title, status, created, updated) VALUES ('G-9','goal nine','active','2026-11-01T00:00:00+01:00','2026-11-01T00:00:00+01:00');
INSERT INTO tasks(id, goal_id, title, spec, acceptance, role, status, assignee, depends_on, branch, pr_url, attempts, created, updated, priority, waits, sandbox_required, tier) VALUES
 ('T-1','G-9','Full task','line one\nline two\n\n  indented','AC 1\nAC 2','coder','in_progress','claude-coder-t-9','["T-0"]','factory/t-1','https://example.test/pr/1',2,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',1,'internal',0,'low'),
 ('T-2','G-9','Blocked on owner','spec two',NULL,'coder','blocked',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'owner',0,NULL),
 ('T-3','G-9','Todo with waits',NULL,'','pm','todo',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'owner',0,NULL),
 ('T-4','G-9','Sandboxed','s',NULL,'coder','todo',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',1,NULL),
 ('T-5','G-9','Not sandboxed','s',NULL,'coder','todo',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',0,NULL),
 ('T-6','G-9','Blank hold','s',NULL,'coder','todo',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',0,NULL),
 ('T-7','G-9','With verdicts','s',NULL,'coder','in_review',NULL,'[]',NULL,'https://example.test/pr/7',1,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',0,NULL),
 ('T-8','G-9','Long history','s',NULL,'coder','in_progress',NULL,'[]',NULL,NULL,1,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',0,NULL),
 ('T-10','G-9','Real hold','s',NULL,'coder','todo',NULL,'[]',NULL,NULL,0,'2026-11-01T10:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal',0,NULL);
UPDATE tasks SET judge_hold_reason='  ' WHERE id='T-6';
UPDATE tasks SET judge_hold_reason='waiting for owner' WHERE id='T-10';
INSERT INTO verdicts(task_id, pr_url, head_sha, role, verdict, summary, author, created) VALUES
 ('T-7','https://example.test/pr/7','0123456789abcdef','reviewer','pass','looks right','claude-reviewer-t-7','2026-11-02T11:00:00+01:00'),
 ('T-7','https://example.test/pr/7',NULL,'qa','fail','missed: ünï','claude-qa-t-7','2026-11-02T12:00:00+01:00');
INSERT INTO agents(name, role, goal_id, team_id, task_id, status, pane_id, model, started, spawn_args, flagless) VALUES
 ('claude-coder-t-9','coder','G-9',NULL,'T-1','live','p1','claude-sonnet-5-5','2026-11-01T10:00:00+01:00','{"runtime":"claude"}',0),
 ('claude-director','director','G-9',NULL,NULL,'live','p2','claude-opus-5-5','2026-11-01T09:00:00+01:00','{"runtime":"claude"}',1),
 ('claude-director-2','director','G-9',NULL,NULL,'live','p3','claude-opus-5-5','2026-11-01T09:00:00+01:00','{"runtime":"claude"}',1);
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-01T10:00:00+01:00','claude-director','task','T-8','add','T-8 added'),
 ('2026-11-02T10:01:00+01:00','claude-coder-t-9','task','T-8','note','first note'),
 ('2026-11-03T10:02:00+01:00','claude-director','task','T-8','update',NULL),
 ('2026-11-04T10:03:00+01:00','claude-director','task','T-8','file-overlap','T-1,T-2'),
 ('2026-11-05T10:04:00+01:00','claude-coder-t-9','task','T-8','note','second note
with two lines'),
 ('2026-11-06T10:05:00+01:00','claude-director','task','T-8','tier','- -> low: ok'),
 ('2026-11-07T10:06:00+01:00','claude-director','task','T-8','task-auto-start','spawn x'),
 ('2026-11-08T10:07:00+01:00','claude-coder-t-9','task','T-8','update','status in_progress'),
 ('2026-11-09T10:08:00+01:00','claude-director','task','T-8','update','attempts 1'),
 ('2026-11-01T10:09:00+01:00','claude-coder-t-9','task','T-8','note','last note');
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES ('2026-11-01T10:00:00+01:00','claude-director','task','T-1','add','Full task'), ('2026-11-02T10:00:00+01:00','claude-coder-t-9','task','T-1','note','started');
