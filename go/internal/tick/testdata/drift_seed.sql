-- Seed of the drift parity fixture. Clock: 2026-11-17T12:00:00+00:00 (TZ UTC). gh answers: pull/88 MERGED, pull/89 OPEN,
-- pull/90 exit 1 "gh: no pull request found".
INSERT INTO goals(id,title,status,created,updated,org) VALUES
 ('G-001','active claude','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-002','planning with work','planning','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-003','active cursor','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','cursor'),
 ('G-004','no lead claude','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-005','no lead cursor','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','cursor'),
 ('G-006','parked lead','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-007','gh failure','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-008','already alerted','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-009','finished','done','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude'),
 ('G-010','cross-org lead','active','2026-11-01T00:00:00+00:00','2026-11-01T00:00:00+00:00','claude');
INSERT INTO agents(name,role,status,goal_id,team_id,task_id,started,ended) VALUES
 ('claude-lead-a','lead','live','G-001','t-a',NULL,'2026-11-17T08:00:00+00:00',NULL),
 ('claude-director','director','live',NULL,NULL,NULL,'2026-11-17T08:00:00+00:00',NULL),
 ('cursor-lead-c','lead','live','G-003','t-c',NULL,'2026-11-17T08:00:00+00:00',NULL),
 ('claude-lead-p','lead','stopped','G-006','t-p',NULL,'2026-11-16T08:00:00+00:00','2026-11-16T09:00:00+00:00'),
 ('cursor-lead-x','lead','live','G-010','t-x',NULL,'2026-11-17T08:00:00+00:00',NULL),
 ('claude-coder-t7','coder','live','G-001','t-a','T-7','2026-11-17T06:00:00+00:00',NULL);
INSERT INTO teams(id,goal_id,lead_agent,status,created,parked_at) VALUES
 ('t-a','G-001','claude-lead-a','active','2026-11-01T00:00:00+00:00',NULL),
 ('t-b','G-002','claude-lead-a','active','2026-11-01T00:00:00+00:00',NULL),
 ('t-c','G-003','cursor-lead-c','active','2026-11-01T00:00:00+00:00',NULL),
 ('t-p','G-006','claude-lead-p','active','2026-11-01T00:00:00+00:00','2026-11-16T09:00:00+00:00'),
 ('t-x','G-010','cursor-lead-x','active','2026-11-01T00:00:00+00:00',NULL);
INSERT INTO tasks(id,goal_id,title,status,depends_on,pr_url,created,updated) VALUES
 ('T-1','G-001','merged pr','in_review','[]','https://github.com/o/r/pull/88','2026-11-01T00:00:00+00:00','2026-11-17T11:00:00+00:00'),
 ('T-2','G-001','open pr','in_review','[]','https://github.com/o/r/pull/89','2026-11-01T00:00:00+00:00','2026-11-17T11:00:00+00:00'),
 ('T-3','G-001','idle todo','todo','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T07:00:00+00:00'),
 ('T-4','G-001','dead dep','todo','["T-5", "T-3"]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T07:00:00+00:00'),
 ('T-5','G-001','cancelled','cancelled','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T07:00:00+00:00'),
 ('T-6','G-001','blocked long','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-16T06:00:00+00:00'),
 ('T-7','G-001','has live agent','in_progress','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T07:00:00+00:00'),
 ('T-8','G-001','waits on T-3','todo','["T-3"]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T07:00:00+00:00'),
 ('T-9','G-001','blocked short','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T06:00:00+00:00'),
 ('T-10','G-002','done work','done','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T06:00:00+00:00'),
 ('T-11','G-002','idle todo not active goal','todo','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-17T01:00:00+00:00'),
 ('T-20','G-003','cursor blocked','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-30','G-004','blocked no lead','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-31','G-005','blocked no lead cursor','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-32','G-006','blocked parked','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-33','G-007','blocked','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-34','G-007','gh fails','in_review','[]','https://github.com/o/r/pull/90','2026-11-01T00:00:00+00:00','2026-11-17T11:00:00+00:00'),
 ('T-40','G-008','blocked already alerted','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-41','G-009','blocked but goal done','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00'),
 ('T-50','G-010','blocked cross-org lead','blocked','[]',NULL,'2026-11-01T00:00:00+00:00','2026-11-15T06:00:00+00:00');
INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES
 ('2026-11-17T11:00:00+00:00','owner','goal','G-008','drift','["G-008", ["D4 T-40 blocked for over 24 h"]]'),
 ('2026-11-17T06:00:00+00:00','owner','task','T-3','note','touched');
