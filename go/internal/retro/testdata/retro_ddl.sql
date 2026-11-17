-- copy of REJECTION_INCIDENTS_DDL and RETRO_DDL in bin/factory_cli.py; tasks is the two columns the retro code reads
CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, pr_url TEXT);
CREATE TABLE IF NOT EXISTS rejection_incidents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  cause TEXT NOT NULL,
  kind TEXT NOT NULL,
  task_id TEXT NOT NULL,
  verdict_id INTEGER,
  event_id INTEGER,
  created TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_rejection_incidents_cause ON rejection_incidents(cause);
CREATE INDEX IF NOT EXISTS idx_rejection_incidents_task ON rejection_incidents(task_id);
CREATE TABLE IF NOT EXISTS retro_runs (
  id INTEGER PRIMARY KEY,
  day TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL,
  agent TEXT,
  stop_reason TEXT,
  started TEXT,
  ended TEXT);
CREATE TABLE IF NOT EXISTS retro_reports (
  id INTEGER PRIMARY KEY,
  day TEXT NOT NULL UNIQUE,
  causes TEXT NOT NULL,
  evidence TEXT NOT NULL,
  fixes TEXT NOT NULL,
  author TEXT NOT NULL,
  created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS adopted_fixes (
  id TEXT PRIMARY KEY,            -- F-001, F-002, ... (zero-padded 3, next = max+1)
  report_day TEXT NOT NULL,       -- retro_reports.day the fix came from
  fix_index INTEGER NOT NULL,     -- 1-based index into that report's fixes
  cause TEXT NOT NULL,            -- copied from the report fix (in REJECTION_CAUSES)
  type TEXT NOT NULL,             -- check | template | prompt_rule (copied)
  text TEXT NOT NULL,             -- fix text (copied)
  signature TEXT NOT NULL,        -- '|'-separated literal terms
  task_id TEXT NOT NULL,          -- the task that implements the fix
  pr_url TEXT,                    -- from tasks.pr_url at adopt / at merge sync
  status TEXT NOT NULL,           -- pending | adopted | held | reopened | noise
  adopted_at TEXT,                -- ts of the task's merged event; NULL while pending
  checked_at TEXT, check_note TEXT, recur_incidents TEXT,  -- written by T-C, NULL here
  author TEXT NOT NULL, created TEXT NOT NULL,
  UNIQUE(report_day, fix_index), UNIQUE(task_id, report_day, fix_index));
