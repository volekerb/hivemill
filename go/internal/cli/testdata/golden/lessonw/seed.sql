-- Python migrate() adds this column on first connect; adding it here keeps the migration out of every db.diff.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO goals(id, title, status, created, updated) VALUES ('G-001', 'lw goal', 'active', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO tasks(id, goal_id, title, status, pr_url, created, updated) VALUES
 ('T-0001', 'G-001', 'merged task', 'done', 'https://github.com/o/r/pull/1', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0002', 'G-001', 'open task', 'todo', NULL, '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00'),
 ('T-0003', 'G-001', 'done task without verdict', 'done', 'https://github.com/o/r/pull/3', '2026-11-01T10:00:00+01:00', '2026-11-01T10:00:00+01:00');
INSERT INTO verdicts(task_id, pr_url, role, verdict, created) VALUES
 ('T-0001', 'https://github.com/o/r/pull/1', 'judge', 'merge', '2026-11-02T09:00:00+01:00');
INSERT INTO agents(name, role, status) VALUES ('lw-lead', 'lead', 'live'), ('lw-coder', 'coder', 'live'), ('lw-director', 'director', 'live');
INSERT INTO lessons(id,title,roles,what,rule,sources,status,recurrences,author,created,updated,applied_at,verified_at,verified_by,evidence) VALUES
 ('L-001','Open, no links','coder','Some context.','Do the thing.','M1,T-1000','open',0,'lw-lead','2026-10-01T10:00:00+01:00','2026-10-01T10:00:00+01:00',NULL,NULL,NULL,NULL),
 ('L-002','Open, one merged one not','lead','x','Rule two.','','open',0,'lw-lead','2026-10-02T10:00:00+01:00','2026-10-02T10:00:00+01:00',NULL,NULL,NULL,NULL),
 ('L-003','Open, merged task link','all',NULL,'Rule three.','#5','open',0,'lw-lead','2026-10-03T10:00:00+01:00','2026-10-03T10:00:00+01:00',NULL,NULL,NULL,NULL),
 ('L-004','Applied','qa','x','Rule four.','M7','applied',1,'lw-lead','2026-10-04T10:00:00+01:00','2026-10-04T10:00:00+01:00','2026-10-04T10:00:00+01:00',NULL,NULL,NULL),
 ('L-005','Verified','qa','x','Rule five.','M7','verified',2,'lw-lead','2026-10-05T10:00:00+01:00','2026-10-05T10:00:00+01:00','2026-10-05T10:00:00+01:00','2026-10-06T10:00:00+01:00','lw-director','evidence five'),
 ('L-006','Open, linked to T-0001','coder','x','Rule six.','','open',0,'lw-lead','2026-10-06T10:00:00+01:00','2026-10-06T10:00:00+01:00',NULL,NULL,NULL,NULL),
 ('L-007','Open, PR-only link','coder','x','Rule seven.','','open',0,'lw-lead','2026-10-07T10:00:00+01:00','2026-10-07T10:00:00+01:00',NULL,NULL,NULL,NULL);
INSERT INTO lesson_links(lesson_id,task_id,pr_url,via,merged_at,author,created) VALUES
 ('L-002','T-0001',NULL,'hook',NULL,'lw-lead','2026-10-02T11:00:00+01:00'),
 ('L-002','T-0002',NULL,NULL,NULL,'lw-lead','2026-10-02T12:00:00+01:00'),
 ('L-003','T-0001',NULL,NULL,NULL,'lw-lead','2026-10-03T11:00:00+01:00'),
 ('L-006','T-0001','https://github.com/o/r/pull/1','cli','2026-11-02T09:00:00+01:00','lw-lead','2026-10-06T11:00:00+01:00'),
 ('L-007',NULL,'https://github.com/o/r/pull/9',NULL,NULL,'lw-lead','2026-10-07T11:00:00+01:00');
