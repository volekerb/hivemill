CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE throttle (key TEXT PRIMARY KEY, ts TEXT);
CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, task_id TEXT, status TEXT NOT NULL,
  model TEXT, session_id TEXT, spawn_args TEXT, started TEXT, ended TEXT, org TEXT);
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT, entity_id TEXT,
  action TEXT NOT NULL, detail TEXT);
CREATE TABLE verdicts (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, verdict TEXT, created TEXT);
