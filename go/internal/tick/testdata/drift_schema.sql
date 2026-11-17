CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE goals (
  id TEXT PRIMARY KEY, project_id TEXT REFERENCES projects(id), title TEXT NOT NULL,
  intent TEXT, success_criteria TEXT, status TEXT NOT NULL DEFAULT 'proposed',
  priority INTEGER NOT NULL DEFAULT 2, requested_by TEXT, workspace_id TEXT,
  created TEXT NOT NULL, updated TEXT NOT NULL,
  org TEXT NOT NULL DEFAULT 'claude', token_limit INTEGER);
CREATE TABLE teams (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), scope TEXT,
  lead_agent TEXT, tab_id TEXT, status TEXT NOT NULL DEFAULT 'active', created TEXT NOT NULL,
  parked_at TEXT, resume_note TEXT);
CREATE TABLE tasks (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), team_id TEXT,
  title TEXT NOT NULL, spec TEXT, acceptance TEXT, role TEXT NOT NULL DEFAULT 'coder',
  status TEXT NOT NULL DEFAULT 'todo', assignee TEXT, depends_on TEXT NOT NULL DEFAULT '[]',
  branch TEXT, worktree TEXT, pr_url TEXT, attempts INTEGER NOT NULL DEFAULT 0,
  created TEXT NOT NULL, updated TEXT NOT NULL, follow_up_of TEXT, priority INTEGER,
  waits TEXT NOT NULL DEFAULT 'internal', sandbox_required INTEGER NOT NULL DEFAULT 0, tier TEXT, judge_hold_reason TEXT);
CREATE TABLE agents (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT,
  status TEXT NOT NULL,            -- queued | starting | live | done | exited | lost | stopped | cancelled
  pane_id TEXT, workspace_id TEXT, tab_id TEXT, model TEXT, session_id TEXT, cwd TEXT,
  spawn_args TEXT, summary TEXT, last_nudge REAL, blocked_since REAL, blocked_alerted INTEGER DEFAULT 0,
  queued_at TEXT, started TEXT, ended TEXT,
  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT,
  stop_requested_at TEXT, stop_attempt_at TEXT, stop_failed_kind TEXT,
  relaunch_requested_at TEXT, relaunch_requested_by TEXT, relaunch_fresh INTEGER NOT NULL DEFAULT 0,
  start_pending REAL, start_pending_info TEXT, org TEXT);
CREATE TABLE throttle (key TEXT PRIMARY KEY, ts TEXT NOT NULL);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT NOT NULL, recipient TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'msg', subject TEXT NOT NULL, body TEXT, goal_id TEXT, task_id TEXT,
  created TEXT NOT NULL, read_at TEXT);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT,
  entity_id TEXT, action TEXT NOT NULL, detail TEXT);
