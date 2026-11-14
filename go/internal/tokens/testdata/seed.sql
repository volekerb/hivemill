INSERT INTO agents(name,role,goal_id,task_id,status,model,session_id,spawn_args,started,ended,org) VALUES
 ('claude-coder-t-0001','coder','G-001','T-0001','exited','claude-sonnet-5-5','sess-aaa','{"runtime":"claude"}','2026-11-10T08:00:00+00:00','2026-11-10T12:00:00+00:00','claude'),
 ('claude-coder-t-0001-r2','coder','G-001','T-0001','exited','claude-sonnet-5-5','sess-none',NULL,'2026-11-10T13:00:00+00:00','2026-11-10T13:30:00+00:00','claude'),
 ('cursor-coder-t-0001','coder','G-001','T-0001','exited','composer-2.5',NULL,'{"runtime":"cursor"}','2026-11-10T09:00:00+00:00','2026-11-10T10:30:00+00:00','cursor'),
 ('claude-pm-g-001','pm','G-001',NULL,'live','claude-opus-5-5','sess-other',NULL,'2026-11-10T14:30:00+00:00',NULL,'claude'),
 ('claude-lead-g-001','lead','G-001',NULL,'live','claude-opus-5-5',NULL,NULL,'2026-11-10T15:30:00+00:00',NULL,NULL),
 ('cursor-lead-g-001','lead','G-001',NULL,'live','grok-4.7-high',NULL,NULL,'2026-11-10T15:00:00+00:00',NULL,NULL),
 ('claude-coder-t-0003','coder','G-002','T-0003','live','claude-sonnet-5-5',NULL,NULL,'2026-11-10T09:00:00+00:00',NULL,'claude');
INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES
 ('2026-11-10T15:40:00+00:00','tick','agent','claude-lead-g-001','session','{"stored": "sess-ddd"}'),
 ('2026-11-10T15:41:00+00:00','tick','agent','claude-lead-g-001','session','not json "stored"'),
 ('2026-11-10T16:00:00+00:00','tick','task','T-0001','judge-reject',NULL);
INSERT INTO verdicts(task_id,verdict,created) VALUES ('T-0001','pass','2026-11-10T12:30:00+00:00'),('T-0001','fail','2026-11-10T12:45:00+00:00'),('T-0001','reject','2026-11-10T20:00:00+00:00');
