INSERT INTO lessons(id,title,roles,what,rule,sources,status,recurrences,author,created,updated,applied_at,verified_at,verified_by,evidence)
VALUES
 ('L-001','Run prcheck before in_review','coder','PRs went to review with FAIL lines.','Run factory prcheck and fix each FAIL.','M605,T-1000','verified',1,'claude-director','2026-10-01T10:00:00+01:00','2026-10-05T10:00:00+01:00','2026-10-02T10:00:00+01:00','2026-10-05T10:00:00+01:00','claude-boss','no recurrence in 14 days'),
 ('L-002','Never merge on a red main','all',NULL,'Stop and report instead.','','open',0,'claude-boss','2026-10-02T10:00:00+01:00','2026-10-02T10:00:00+01:00',NULL,NULL,NULL,NULL),
 ('L-003','Leads route, they do not code','lead,director','Leads wrote code twice.','Ask the coder.','#12','applied',0,'claude-boss','2026-10-03T10:00:00+01:00','2026-10-04T10:00:00+01:00','2026-10-04T10:00:00+01:00',NULL,NULL,NULL),
 ('L-004','Ünïcode title with a long tail that gets cut at sixty characters when the table is printed','qa,reviewer','Line one.
Line two.','Check both.','','open',2,NULL,'2026-10-04T10:00:00+01:00','2026-10-04T10:00:00+01:00',NULL,NULL,NULL,NULL);
INSERT INTO lesson_links(lesson_id,task_id,pr_url,via,merged_at,author,created)
VALUES
 ('L-001','T-1000','https://github.com/o/r/pull/5','prompt','2026-10-02T09:00:00+01:00','claude-director','2026-10-02T09:30:00+01:00'),
 ('L-001',NULL,'https://github.com/o/r/pull/6',NULL,NULL,NULL,'2026-10-03T09:30:00+01:00'),
 ('L-003','T-1001',NULL,'hook',NULL,'claude-boss','2026-10-04T09:30:00+01:00');
INSERT INTO events(ts,actor,entity,entity_id,action,detail)
VALUES
 ('2026-10-01T10:00:00+01:00','claude-director','lesson','L-001','add','Run prcheck before in_review'),
 ('2026-10-02T10:00:00+01:00','claude-director','lesson','L-001','applied',NULL),
 ('2026-10-05T10:00:00+01:00','claude-boss','lesson','L-001','verified','no recurrence in 14 days'),
 ('2026-10-03T10:00:00+01:00','claude-boss','lesson','L-002','add','Never merge on a red main');
