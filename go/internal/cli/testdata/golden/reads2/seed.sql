INSERT INTO projects(id,name,repo_path,gh_repo,base_branch,auto_merge,created,required_checks,gate_cmd,gate_count_cmd,gate_timeout_s) VALUES
 ('p1','Project One','/srv/p1','o/p1','main',1,'2026-11-01T00:00:00+00:00','ci,lint','make test','make count',600),
 ('p2','Project Two',NULL,NULL,'dev',0,'2026-11-01T00:00:00+00:00','','','',3600);
INSERT INTO goals(id, title, status, priority, created, updated, org) VALUES
 ('G-1','One','active',1,'2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-2','Two','active',1,'2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','cursor'),
 ('G-3','Three','active',1,'2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-4','Four','active',1,'2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-5','Five','active',1,'2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude');
INSERT INTO teams(id,goal_id,scope,lead_agent,status,created) VALUES
 ('g-1-a','G-1','scope a','claude-lead-g-1-a','active','2026-11-01T00:00:00+00:00'),
 ('g-1-b','G-1','scope b — ünï','claude-lead-g-1-b','active','2026-11-01T00:00:00+00:00'),
 ('g-1-c','G-1',NULL,NULL,'closed','2026-11-01T00:00:00+00:00');
INSERT INTO evaluations(team_id,score,created) VALUES
 ('g-1-a',4,'2026-11-02'),('g-1-a',4,'2026-11-03'),('g-1-a',4,'2026-11-04'),
 ('g-1-b',3,'2026-11-02'),('g-1-b',3,'2026-11-03'),('g-1-b',4,'2026-11-04');
INSERT INTO agents(name,role,status) VALUES ('claude-retro-1','retro','live');
INSERT INTO owner_alert_feed(org,kind,ref,since,link,first_seen,closed) VALUES
 ('claude','pr-stuck','T-1','2026-11-01T01:00:00+00:00','https://x/1','2026-11-01T01:00:00+00:00',NULL),
 ('cursor','blocked','T-2','2026-11-01T02:00:00+00:00','https://x/2 — ünï "q"','2026-11-01T02:00:00+00:00',NULL),
 ('claude','gone','T-3','2026-11-01T03:00:00+00:00','https://x/3','2026-11-01T03:00:00+00:00','2026-11-02T00:00:00+00:00');
INSERT INTO gate_runs(project_id,task_id,pr_url,kind,head_sha,main_sha,status,exit_code,test_count,main_count,cmd,log_path,actor,note,created,started,finished) VALUES
 ('p1','T-1','https://x/pr/1','pr','aaa111','bbb222','pass',0,120,118,'make test','/l/1.log','tick','all good','2026-11-01T00:00:00+00:00','2026-11-01T00:00:01+00:00','2026-11-01T00:05:00+00:00'),
 ('p1','T-1','https://x/pr/1','pr','aaa112','bbb222','fail',1,NULL,NULL,'make test','/l/2.log','tick',NULL,'2026-11-01T01:00:00+00:00','2026-11-01T01:00:01+00:00','2026-11-01T01:05:00+00:00'),
 ('p2',NULL,NULL,'main',NULL,NULL,'queued',NULL,NULL,NULL,NULL,NULL,NULL,'','2026-11-01T02:00:00+00:00',NULL,NULL),
 ('p1','T-2',NULL,'pr','ccc333','ddd444','pass',0,50,NULL,'make test','/l/4.log','tick','','2026-11-01T03:00:00+00:00',NULL,NULL);
INSERT INTO goal_token_tally(goal_id,input,output,cache_read,cache_write_5m,cache_write_1h,cost,computed_at) VALUES
 ('G-1',100,200,999999,30,4,0.005,'2026-11-01T00:00:00+00:00'),
 ('G-4',1,2,3,4,5,2.675,'2026-11-01T00:00:00+00:00'),
 ('G-5',10,20,30,40,50,NULL,'2026-11-01T05:00:00+00:00'),
 ('unattributed',7,8,9,10,11,NULL,'2026-11-01T00:00:00+00:00');
