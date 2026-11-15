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
