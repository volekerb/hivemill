CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, repo_path TEXT, gh_repo TEXT,
  base_branch TEXT NOT NULL DEFAULT 'main', auto_merge INTEGER NOT NULL DEFAULT 1,
  notes TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS goals (
  id TEXT PRIMARY KEY, project_id TEXT REFERENCES projects(id), title TEXT NOT NULL,
  intent TEXT, success_criteria TEXT, status TEXT NOT NULL DEFAULT 'proposed',
  priority INTEGER NOT NULL DEFAULT 2, requested_by TEXT, workspace_id TEXT,
  created TEXT NOT NULL, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS plans (
  id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id),
  version INTEGER NOT NULL, body TEXT NOT NULL, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS teams (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), scope TEXT,
  lead_agent TEXT, tab_id TEXT, status TEXT NOT NULL DEFAULT 'active', created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, goal_id TEXT NOT NULL REFERENCES goals(id), team_id TEXT,
  title TEXT NOT NULL, spec TEXT, acceptance TEXT, role TEXT NOT NULL DEFAULT 'coder',
  status TEXT NOT NULL DEFAULT 'todo', assignee TEXT, depends_on TEXT NOT NULL DEFAULT '[]',
  branch TEXT, worktree TEXT, pr_url TEXT, attempts INTEGER NOT NULL DEFAULT 0,
  created TEXT NOT NULL, updated TEXT NOT NULL, follow_up_of TEXT, waits TEXT NOT NULL DEFAULT 'internal');
CREATE TABLE IF NOT EXISTS agents (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT,
  status TEXT NOT NULL,            -- queued | live | done | exited | lost | stopped
  pane_id TEXT, workspace_id TEXT, tab_id TEXT, model TEXT, session_id TEXT, cwd TEXT,
  spawn_args TEXT, summary TEXT, last_nudge REAL, blocked_since REAL, blocked_alerted INTEGER DEFAULT 0,
  queued_at TEXT, started TEXT, ended TEXT,
  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT,
  stop_requested_at TEXT, stop_attempt_at TEXT, stop_failed_kind TEXT);
CREATE TABLE IF NOT EXISTS throttle (key TEXT PRIMARY KEY, ts TEXT NOT NULL);
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
CREATE TABLE IF NOT EXISTS audits (
  id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id),
  version INTEGER NOT NULL, verdict TEXT NOT NULL,      -- ready | gaps
  body TEXT NOT NULL, author TEXT, task_id TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS signoffs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id),
  audit_id INTEGER NOT NULL REFERENCES audits(id), snapshot TEXT NOT NULL,   -- JSON {task_id: status}
  reason TEXT, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT,
  entity_id TEXT, action TEXT NOT NULL, detail TEXT);
CREATE INDEX IF NOT EXISTS idx_msg_rcpt ON messages(recipient, read_at);
CREATE INDEX IF NOT EXISTS idx_tasks_goal ON tasks(goal_id, status);
