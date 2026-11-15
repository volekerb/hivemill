-- Seed for TestJudgeGateEntry: one project, one goal, one in_review task with a PR and one judge agent.
ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT;
INSERT INTO projects(id,name,gh_repo,created) VALUES('P-1','proj','o/r','2026-11-12T00:00:00+00:00');
INSERT INTO goals(id,project_id,title,status,created,updated) VALUES('G-1','P-1','goal','active','2026-11-12T00:00:00+00:00','2026-11-12T00:00:00+00:00');
INSERT INTO tasks(id,goal_id,title,status,assignee,branch,pr_url,created,updated) VALUES('T-0001','G-1','task','in_review','claude-coder-t-1','factory/t-0001','https://github.com/o/r/pull/7','2026-11-12T00:00:00+00:00','2026-11-12T00:00:00+00:00');
INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('claude-judge','judge','G-1','T-0001','live');
INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('claude-coder-t-1','coder','G-1','T-0001','live');
