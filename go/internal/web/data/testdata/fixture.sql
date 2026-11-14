-- Seeded fixture for the web data-layer tests (T-1474). Executed by the Go tests and by the one-off Python that
-- produced testdata/golden/*.json. "Now" for every time-dependent query is 2026-11-14T12:00:00+01:00, TZ=UTC.
CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, repo_path TEXT, gh_repo TEXT,
  base_branch TEXT NOT NULL DEFAULT 'main', auto_merge INTEGER NOT NULL DEFAULT 1,
  notes TEXT, created TEXT NOT NULL, required_checks TEXT NOT NULL DEFAULT '',
  gate_cmd TEXT NOT NULL DEFAULT '', gate_count_cmd TEXT NOT NULL DEFAULT '', gate_timeout_s INTEGER NOT NULL DEFAULT 3600);
CREATE TABLE IF NOT EXISTS goals (
  id TEXT PRIMARY KEY, project_id TEXT REFERENCES projects(id), title TEXT NOT NULL,
  intent TEXT, success_criteria TEXT, status TEXT NOT NULL DEFAULT 'proposed',
  priority INTEGER NOT NULL DEFAULT 2, requested_by TEXT, workspace_id TEXT,
  created TEXT NOT NULL, updated TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT 'claude', token_limit INTEGER);
CREATE TABLE IF NOT EXISTS teams (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), scope TEXT,
  lead_agent TEXT, tab_id TEXT, status TEXT NOT NULL DEFAULT 'active', created TEXT NOT NULL,
  parked_at TEXT, resume_note TEXT);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), team_id TEXT,
  title TEXT NOT NULL, spec TEXT, acceptance TEXT, role TEXT NOT NULL DEFAULT 'coder',
  status TEXT NOT NULL DEFAULT 'todo', assignee TEXT, depends_on TEXT NOT NULL DEFAULT '[]',
  branch TEXT, worktree TEXT, pr_url TEXT, attempts INTEGER NOT NULL DEFAULT 0,
  created TEXT NOT NULL, updated TEXT NOT NULL, follow_up_of TEXT, priority INTEGER,
  waits TEXT NOT NULL DEFAULT 'internal', sandbox_required INTEGER NOT NULL DEFAULT 0, tier TEXT);
CREATE TABLE IF NOT EXISTS agents (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT,
  status TEXT NOT NULL,
  pane_id TEXT, workspace_id TEXT, tab_id TEXT, model TEXT, session_id TEXT, cwd TEXT,
  spawn_args TEXT, summary TEXT, last_nudge REAL, blocked_since REAL, blocked_alerted INTEGER DEFAULT 0,
  queued_at TEXT, started TEXT, ended TEXT,
  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT,
  stop_requested_at TEXT, stop_attempt_at TEXT, stop_failed_kind TEXT,
  relaunch_requested_at TEXT, relaunch_requested_by TEXT, relaunch_fresh INTEGER NOT NULL DEFAULT 0,
  start_pending REAL, start_pending_info TEXT, org TEXT);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT NOT NULL, recipient TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'msg', subject TEXT NOT NULL, body TEXT, goal_id TEXT, task_id TEXT,
  created TEXT NOT NULL, read_at TEXT);
CREATE TABLE IF NOT EXISTS verdicts (
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, pr_url TEXT, head_sha TEXT,
  role TEXT NOT NULL, verdict TEXT NOT NULL, summary TEXT, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS evaluations (
  id INTEGER PRIMARY KEY AUTOINCREMENT, team_id TEXT NOT NULL, score INTEGER NOT NULL,
  notes TEXT, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT,
  entity_id TEXT, action TEXT NOT NULL, detail TEXT);
CREATE TABLE IF NOT EXISTS rejection_incidents (
  id INTEGER PRIMARY KEY AUTOINCREMENT, cause TEXT NOT NULL, kind TEXT NOT NULL, task_id TEXT NOT NULL,
  verdict_id INTEGER, event_id INTEGER, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS util_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, live INTEGER NOT NULL, workers INTEGER NOT NULL,
  leads INTEGER NOT NULL, judges INTEGER NOT NULL, hq INTEGER NOT NULL, other INTEGER NOT NULL, queued INTEGER NOT NULL,
  queued_workers INTEGER NOT NULL, cap INTEGER NOT NULL, target INTEGER NOT NULL, demand INTEGER NOT NULL,
  p1_starved TEXT NOT NULL, p2_starved TEXT NOT NULL);

INSERT INTO config(key,value) VALUES ('max_live_agents','6'),('min_mem_available_mb','2048'),('max_five_hour_pct','90'),('note','x');
INSERT INTO projects(id,name,created) VALUES ('P-1','factory','2026-10-01T09:00:00+02:00');

INSERT INTO goals(id,project_id,title,intent,success_criteria,status,priority,created,updated,org) VALUES
 ('G-001','P-1','Claude goal','Intent one','1. Build X
2. Ship Y
   continued line
3. Untracked item','active',1,'2026-11-01T08:00:00+01:00','2026-11-14T09:00:00+01:00','claude'),
 ('G-002','P-1','Cursor goal',NULL,'first free line
second free line','active',2,'2026-11-02T08:00:00+01:00','2026-11-13T09:00:00+01:00','cursor'),
 ('G-003',NULL,'Done goal','x','1. only','done',3,'2026-10-01T08:00:00+02:00','2026-10-02T09:00:00+02:00','claude'),
 ('G-004','P-1','Proposed goal','p',NULL,'proposed',1,'2026-11-14T08:00:00+01:00','2026-11-14T08:30:00+01:00','weird');
UPDATE goals SET intent = 'Long intent é ' || replace(hex(zeroblob(400)), '00', 'ab') WHERE id='G-003';

INSERT INTO teams(id,goal_id,scope,lead_agent,status,created) VALUES
 ('tm-a','G-001','core','claude-lead-g-001','active','2026-11-01T08:10:00+01:00'),
 ('tm-b','G-001',NULL,NULL,'parked','2026-11-01T08:20:00+01:00'),
 ('tm-c','G-002','port','cursor-lead-g-002','active','2026-11-02T08:10:00+01:00');
INSERT INTO evaluations(team_id,score,created) VALUES ('tm-a',70,'2026-11-10T10:00:00+01:00'),('tm-a',85,'2026-11-12T10:00:00+01:00'),('tm-c',60,'2026-11-12T10:00:00+01:00');

INSERT INTO tasks(id,goal_id,team_id,title,role,status,assignee,depends_on,pr_url,attempts,created,updated,follow_up_of,waits) VALUES
 ('T-1','G-001','tm-a','[SC1] build parser','coder','done','claude-coder-t-1','[]','https://x/pr/1',1,'2026-11-01T09:00:00+01:00','2026-11-02T10:00:00+01:00',NULL,'internal'),
 ('T-2','G-001','tm-a','[SC2] wire cli','coder','in_review','claude-coder-t-2','["T-1"]','https://x/pr/2',2,'2026-11-02T09:00:00+01:00','2026-11-13T10:00:00+01:00',NULL,'internal'),
 ('T-3','G-001','tm-b','[SC2,SC3] docs','coder','blocked',NULL,'["T-1","T-2"]',NULL,0,'2026-11-03T09:00:00+01:00','2026-11-13T11:00:00+01:00',NULL,'owner'),
 ('T-4','G-001',NULL,'[SC1] loose follow','coder','cancelled',NULL,'[]',NULL,0,'2026-11-03T10:00:00+01:00','2026-11-04T10:00:00+01:00','T-1','internal'),
 ('T-5','G-001','tm-a','plain no tag','coder','done','claude-coder-t-5','[]','https://x/pr/5',3,'2026-11-05T09:00:00+01:00','2026-11-12T12:00:00+00:00',NULL,'internal'),
 ('T-6','G-002','tm-c','Cursor port','coder','in_progress','cursor-coder-t-6','[]',NULL,1,'2026-11-06T09:00:00+01:00','2026-11-14T09:00:00+01:00',NULL,'internal'),
 ('T-7','G-002',NULL,'not json deps','coder','todo',NULL,'not json',NULL,0,'2026-11-06T10:00:00+01:00','2026-11-06T10:00:00+01:00',NULL,'internal'),
 ('T-8','G-003',NULL,'old merged','coder','done',NULL,'{"a":1}','https://x/pr/8',1,'2026-10-01T09:00:00+02:00','2026-10-01T12:00:00+02:00',NULL,'internal'),
 ('T-9','G-001','tm-a','blocked internal [SC2]','coder','blocked',NULL,'[]',NULL,1,'2026-11-07T09:00:00+01:00','2026-11-14T08:00:00+01:00',NULL,'internal'),
 ('T-10','G-001',NULL,'pm spec task','pm','todo',NULL,'[]',NULL,0,'2026-11-01T08:30:00+01:00','2026-11-01T08:30:00+01:00',NULL,'internal'),
 ('T-11','G-002','tm-c','[SC1] é ünïcode 日本  spaced','coder','done','cursor-coder-t-11','[1, true, null]','https://x/pr/11',1,'2026-11-09T09:00:00+01:00','2026-11-11T09:00:00+01:00',NULL,'internal'),
 ('T-12','G-001','tm-zz','ghost team','coder','todo',NULL,'[]',NULL,0,'2026-11-10T09:00:00+01:00','2026-11-10T09:00:00+01:00',NULL,'internal'),
 ('T-55','G-001',NULL,'name collision','coder','todo',NULL,'[]',NULL,0,'2026-11-10T09:00:00+01:00','2026-11-10T09:00:00+01:00',NULL,'internal');

INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,pane_id,session_id,spawn_args,queued_at,started,ended,stop_requested_at,org) VALUES
 ('claude-boss','boss',NULL,NULL,NULL,'live','claude-opus-5-5','p1','s-boss',NULL,NULL,'2026-11-14T10:00:00+01:00',NULL,NULL,NULL),
 ('claude-coder-t-2','coder','G-001','tm-a','T-2','live','claude-sonnet-5-5','p2','s-t2','{"runtime":"claude","kickoff":"secret text"}',NULL,'2026-11-14T11:00:00+01:00',NULL,NULL,'claude'),
 ('cursor-coder-t-6','coder','G-002','tm-c','T-6','live','composer-2.5','p3','s-t6','{"runtime":"cursor","kickoff":"secret"}',NULL,'2026-11-14T11:30:00+01:00',NULL,'2026-11-14T11:59:00+01:00','cursor'),
 ('claude-pm-g-001','pm','G-001',NULL,'T-10','done','claude-opus-5-5',NULL,'s-pm','not json',NULL,'2026-11-01T08:30:00+01:00','2026-11-01T08:50:00+01:00',NULL,NULL),
 ('legacy-agent','coder','G-002','tm-c',NULL,'exited','claude-sonnet-5-5',NULL,NULL,'{"runtime":"bogus"}',NULL,'2026-11-10T08:00:00+01:00','2026-11-10T09:00:00+01:00',NULL,NULL),
 ('claude-coder-t-1','coder','G-001','tm-a','T-1','done','claude-sonnet-5-5',NULL,'s-t1',NULL,NULL,'2026-11-01T10:00:00+01:00','2026-11-02T09:30:00+01:00',NULL,NULL),
 ('claude-coder-t-5','coder','G-001','tm-a','T-5','done','claude-sonnet-5-5',NULL,'s-t5',NULL,NULL,'2026-11-05T10:00:00+01:00','2026-11-08T09:30:00+01:00',NULL,NULL),
 ('claude-reviewer-t-2','reviewer','G-001','tm-a','T-2','done','claude-opus-5-5',NULL,'s-r2',NULL,NULL,'2026-11-12T10:00:00+01:00','2026-11-12T10:30:00+01:00',NULL,NULL),
 ('claude-coder-t-10','coder','G-001',NULL,'T-10','queued',NULL,NULL,NULL,NULL,'2026-11-14T11:55:00+01:00',NULL,NULL,NULL,NULL),
 ('claude-lead-g-001','lead','G-001','tm-a',NULL,'blocked','claude-opus-5-5',NULL,'s-lead',NULL,NULL,'2026-11-01T08:10:00+01:00',NULL,NULL,'claude'),
 ('claude-coder-t-11','coder','G-002','tm-c','T-11','lost',NULL,NULL,NULL,NULL,'2026-11-09T09:30:00',NULL,NULL,NULL,NULL);

INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES
 ('2026-11-01T09:00:00+01:00','claude-pm-g-001','task','T-1','add','title=x'),
 ('2026-11-01T10:00:00+01:00','claude-boss','task','T-1','update','status=in_progress'),
 ('2026-11-02T09:00:00+01:00','claude-coder-t-1','task','T-1','update','status=in_review pr=https://x/pr/1'),
 ('2026-11-02T09:40:00+01:00','claude-judge','task','T-1','judge-blocked','checks not green: lint'),
 ('2026-11-02T10:00:00+01:00','claude-judge','task','T-1','merged','@abc1234'),
 ('2026-11-02T09:00:00+01:00','claude-pm-g-001','task','T-2','add','title=y'),
 ('2026-11-02T10:00:00+01:00','claude-coder-t-2','task','T-2','update','status=in_progress'),
 ('2026-11-12T09:00:00+01:00','claude-coder-t-2','task','T-2','update','status=in_review pr=https://x/pr/2'),
 ('2026-11-12T09:10:00+01:00','factory','task','T-2','review-autospawn-wait','waiting for ci'),
 ('2026-11-12T09:20:00+01:00','factory','task','T-2','review-autospawn-wait','waiting for ci'),
 ('2026-11-12T09:30:00+01:00','factory','task','T-2','review-autospawn','spawned reviewer'),
 ('2026-11-12T10:40:00+01:00','claude-judge','task','T-2','judge-reject','stale verdict'),
 ('2026-11-12T11:00:00+01:00','claude-boss','task','T-2','reopen','back to coder'),
 ('2026-11-13T09:00:00+01:00','claude-coder-t-2','task','T-2','update','status=in_review'),
 ('2026-11-13T09:30:00+01:00','claude-judge','task','T-2','judge-blocked','auto_merge=0 owner wants to look'),
 ('2026-11-13T09:40:00+01:00','claude-judge','task','T-2','judge-blocked','auto_merge=0 again; checks not green'),
 ('2026-11-03T09:00:00+01:00','claude-boss','task','T-3','update','status=blocked waits=owner'),
 ('2026-11-03T12:00:00+01:00','claude-boss','task','T-3','note','a note'),
 ('2026-11-05T09:00:00+01:00','claude-coder-t-5','task','T-5','update','status=in_progress'),
 ('2026-11-06T09:00:00+01:00','claude-coder-t-5','task','T-5','update','status=in_review pr=https://x/pr/5'),
 ('2026-11-07T09:00:00+01:00','claude-coder-t-5','task','T-5','update','status=in_review'),
 ('2026-11-08T09:00:00+01:00','claude-coder-t-5','task','T-5','update','status=in_review'),
 ('2026-11-08T10:00:00+01:00','claude-judge','task','T-5','merged','@deadbee'),
 ('2026-11-12T11:00:00','claude-judge','task','T-5','merged','@feed001'),
 ('2026-11-07T10:00:00+01:00','claude-judge','task','T-5','judge-reject','conflicts'),
 ('2026-11-07T10:10:00+01:00','claude-judge','task','T-5','judge-reject','conflicts again'),
 ('2026-11-12T08:00:00+01:00','claude-boss','task','T-9','update','status=blocked waits=owner'),
 ('2026-11-12T09:00:00+01:00','claude-boss','task','T-9','update','status=blocked'),
 ('2026-11-12T10:00:00+01:00','claude-boss','task','T-9','update','status=todo'),
 ('2026-11-13T10:00:00+01:00','claude-boss','task','T-9','update','status=blocked'),
 ('2026-11-09T10:00:00+01:00','cursor-coder-t-11','task','T-11','update','pr=https://x/pr/11'),
 ('2026-11-11T09:00:00Z','cursor-judge','task','T-11','merged','abc'),
 ('2026-10-01T11:00:00+02:00','claude-judge','task','T-8','merged','@old0001'),
 ('2026-11-14T11:55:00+01:00','factory','agent','claude-coder-t-10','queued','at cap: 6 live'),
 ('2026-11-14T11:56:00+01:00','factory','agent','claude-coder-t-10','queued','at cap again'),
 ('2026-11-14T11:30:00+01:00','owner','config','max_live_agents','set','6'),
 ('2026-11-14T10:00:00+01:00','claude-boss','goal','G-001','note','goal note'),
 ('2026-11-14T11:45:00+01:00','claude-boss','agent','claude-boss','session','{"stored":"s-boss"}'),
 ('2026-11-14T11:46:00+01:00','factory','agent','claude-lead-g-001','session','{"herdr":"s-herdr-lead"}'),
 ('2026-11-14T11:47:00+01:00','factory','agent','claude-lead-g-001','session','not json {"stored":'),
 ('2026-11-14T11:50:00+01:00','claude-coder-t-2','task','T-2','note','x'),
 ('2026-11-14T11:58:00+01:00','claude-boss','goal','G-002','note','g2 note');
UPDATE events SET detail = detail || ' ' || replace(hex(zeroblob(200)), '00', 'zz') WHERE action='judge-reject' AND entity_id='T-5' AND detail='conflicts';

INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES
 ('T-1','https://x/pr/1','1111111111111111','reviewer','pass','ok','claude-reviewer-t-1','2026-11-02T09:20:00+01:00'),
 ('T-1','https://x/pr/1','1111111111111111','qa','pass','ok','claude-qa-t-1','2026-11-02T09:25:00+01:00'),
 ('T-1','https://x/pr/1','1111111111111111','judge','merge','merged','claude-judge','2026-11-02T10:00:00+01:00'),
 ('T-2','https://x/pr/2','2222222222','reviewer','fail','bug here','claude-reviewer-t-2','2026-11-12T10:20:00+01:00'),
 ('T-2','https://x/pr/2','2222222222','judge','reject','rejected','claude-judge','2026-11-12T10:40:00+01:00'),
 ('T-2','https://x/pr/2',NULL,'qa','pass',NULL,NULL,'2026-11-13T09:20:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','reviewer','fail','x','claude-reviewer-t-5','2026-11-06T10:00:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','qa','reject','y','claude-qa-t-5','2026-11-07T09:30:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','reviewer','pass','ok','claude-reviewer-t-5','2026-11-08T09:30:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','qa','pass','ok','claude-qa-t-5','2026-11-08T09:40:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','judge','merge','merged','claude-judge','2026-11-08T10:00:00+01:00'),
 ('T-5','https://x/pr/5','5555555555555555','judge','Merge','merged again','claude-judge','2026-11-12T11:00:00'),
 ('T-11','https://x/pr/11','1111','judge','merge','m','cursor-judge','2026-11-11T09:00:00Z'),
 ('T-8','https://x/pr/8','8888','judge','merge','m','claude-judge','2026-10-01T11:00:00+02:00'),
 ('T-9',NULL,NULL,'reviewer','fail','blocked one','claude-reviewer-t-9','2026-11-12T07:00:00+01:00');

INSERT INTO rejection_incidents(cause,kind,task_id,created) VALUES
 ('stale-verdict','judge','T-2','2026-11-12T10:40:00+01:00'),('conflict','judge','T-5','2026-11-07T10:00:00+01:00'),
 ('stale-verdict','judge','T-5','2026-11-07T10:10:00+01:00'),('lint','reviewer','T-8','2026-10-01T09:00:00+02:00');

INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created,read_at) VALUES
 ('claude-lead-g-001','claude-boss','escalation','Need a call on T-2','body secret','G-001','T-2','2026-11-14T09:00:00+01:00',NULL),
 ('claude-lead-g-001','owner','request','Please approve','b','G-001',NULL,'2026-11-14T09:30:00+01:00',NULL),
 ('cursor-lead-g-002','cursor-boss','request','Cursor ask','b','G-002','T-6','2026-11-14T09:40:00+01:00',NULL),
 ('claude-lead-g-001','claude-boss','escalation','already read','b','G-001',NULL,'2026-11-13T09:40:00+01:00','2026-11-13T10:00:00+01:00'),
 ('claude-lead-g-001','claude-director','escalation','not for boss','b','G-001',NULL,'2026-11-14T09:50:00+01:00',NULL),
 ('claude-boss','claude-director','decision','Decision about T-5 and T-55x','Run T-9, not T-99 nor aT-9 nor T-9b; also (T-12) fine.','G-001',NULL,'2026-11-10T09:00:00+01:00',NULL),
 ('boss','claude-director','decision','Owner decision','names T-3.','G-001','T-1','2026-11-11T09:00:00+01:00',NULL),
 ('cursor-boss','cursor-director','decision','x','T-6','G-002',NULL,'2026-11-11T09:00:00+01:00',NULL),
 ('claude-director','claude-boss','report','A report with a very long subject line that goes on and on and on past one hundred and twenty characters so that the cut applies here',NULL,'G-001',NULL,'2026-11-12T09:00:00+01:00','2026-11-12T09:10:00+01:00'),
 ('factory','owner','alert','alert one',NULL,NULL,NULL,'2026-11-12T10:00:00+01:00',NULL),
 ('a','b','msg','plain msg',NULL,NULL,NULL,'2026-11-12T10:00:00+01:00',NULL);

INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) VALUES
 ('2026-11-14T11:00:00+01:00',3,1,1,0,1,0,0,0,6,3,1,'[]','[]'),
 ('2026-11-14T11:58:30+01:00',4,1,1,1,1,0,1,1,6,3,1,'["G-001","G-002"]','[]');
