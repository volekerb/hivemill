INSERT OR REPLACE INTO config(key,value) VALUES('min_mem_available_mb','1'),('terminal_backend','herdr'),('max_live_agents','12'),('lead_parking','1'),('lead_less_dispatch','shadow');
INSERT INTO goals(id,project_id,title,status,priority,created,updated) VALUES
 ('G-001',NULL,'one','active',1,'2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('G-002',NULL,'two','active',1,'2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('G-003',NULL,'three','planning',2,'2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('G-004',NULL,'four','active',1,'2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00');
INSERT INTO tasks(id,goal_id,title,role,status,depends_on,waits,created,updated) VALUES
 ('T-1','G-001','a','coder','todo','[]','internal','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('T-2','G-002','b','coder','in_review','[]','internal','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('T-3','G-004','c','coder','blocked','[]','owner','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('T-4','G-004','d','pm','blocked','["T-9"]','internal','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('T-5','G-003','e','coder','todo','["T-6"]','internal','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00'),
 ('T-6','G-003','f','coder','done','[]','internal','2026-11-10T00:00:00+00:00','2026-11-10T00:00:00+00:00');
INSERT INTO agents(name,role,goal_id,task_id,status,pane_id,started,ended,session_id,spawn_args,org) VALUES
 ('claude-boss','boss',NULL,NULL,'live','p1','2026-11-11T00:00:00+00:00',NULL,'s-boss',NULL,NULL),
 ('claude-director','director',NULL,NULL,'live','p2','2026-11-11T00:00:00+00:00',NULL,'s-dir','{"runtime":"claude"}',NULL),
 ('claude-lead-g-001','lead','G-001',NULL,'live','p3','2026-11-11T00:00:00+00:00',NULL,'s-lead',NULL,NULL),
 ('claude-coder-t-9','coder','G-002','T-9','live','p4','2026-11-11T00:00:00+00:00',NULL,'s-c9',NULL,NULL),
 ('claude-judge-g-002','judge','G-002',NULL,'live','p5','2026-11-11T00:00:00+00:00',NULL,'s-j',NULL,NULL),
 ('cursor-coder-t-7','coder','G-002','T-7','live','p6','2026-11-11T00:00:00+00:00',NULL,'s-c7','{"runtime":"cursor","cursor_workspace":"/nonexistent/ws"}','cursor'),
 ('claude-pm-g-003','pm','G-003',NULL,'queued',NULL,NULL,NULL,NULL,NULL,NULL),
 ('claude-lead-g-004','lead','G-004',NULL,'queued',NULL,NULL,NULL,NULL,NULL,NULL),
 ('claude-coder-old','coder','G-004','T-3','done','p9','2026-11-09T00:00:00+00:00','2026-11-10T20:00:00+00:00','s-old',NULL,NULL);
INSERT INTO gate_runs(project_id,task_id,kind,status,pid,created) VALUES
 ('proj',NULL,'main','running',77,'2026-11-10T00:00:00+00:00'),('proj','T-1','pr','running',78,'2026-11-10T00:00:00+00:00'),('proj','T-2','pr','pass',79,'2026-11-10T00:00:00+00:00');
INSERT INTO messages(sender,recipient,kind,subject,created) VALUES
 ('claude-director','boss','escalation','Owner action needed: rotate key','2026-11-12T00:30:00+00:00'),
 ('claude-director','boss','escalation','owner action: second','2026-11-11T22:00:00+00:00'),
 ('boss','owner','decision','answered M2 by owner','2026-11-12T00:40:00+00:00');
INSERT INTO teams(id,goal_id,lead_agent,status,created,parked_at) VALUES ('TM-1','G-004',NULL,'active','2026-11-10T00:00:00+00:00','2026-11-12T00:00:00+00:00');
