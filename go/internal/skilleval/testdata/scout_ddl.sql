
CREATE TABLE IF NOT EXISTS scout_candidates (
  id TEXT PRIMARY KEY,            -- C-0001
  key TEXT NOT NULL UNIQUE,       -- 'gh:<owner>/<repo>[/<path>]' or 'mkt:<marketplace>/<plugin>'
  kind TEXT,                      -- skill | plugin | agent | hook | mcp | collection | unknown
  repo TEXT, path TEXT, url TEXT,
  licence TEXT, last_commit TEXT, default_branch TEXT, head_sha TEXT,
  stars INTEGER, stars_per_day REAL,
  install_risk TEXT,              -- prompt-only | scripts | hooks | mcp | network | shell-in-markdown | symlinks | unknown
  risk_detail TEXT,               -- JSON: offending paths (max 20)
  sources TEXT NOT NULL DEFAULT '[]',
  first_seen TEXT NOT NULL, last_seen TEXT NOT NULL, seen_count INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'new');  -- new | triaged | needs-owner | queued-eval | evaluated | dismissed
CREATE TABLE IF NOT EXISTS scout_triage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL REFERENCES scout_candidates(id),
  purpose TEXT NOT NULL, target_roles TEXT NOT NULL, overlap TEXT NOT NULL,
  recommendation TEXT NOT NULL,   -- adopt | watch | skip
  rationale TEXT NOT NULL, eval_suggested INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scout_stars (
  candidate_id TEXT NOT NULL, ts TEXT NOT NULL, stars INTEGER NOT NULL, PRIMARY KEY(candidate_id, ts));
CREATE TABLE IF NOT EXISTS scout_cursors (
  source TEXT PRIMARY KEY, cursor TEXT, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scout_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, slot TEXT NOT NULL UNIQUE,   -- '2026-10-25T09'; UNIQUE = no double run per slot
  agent TEXT, status TEXT NOT NULL,      -- claimed | queued | running | done | stopped | failed | skipped
  started TEXT, ended TEXT, tokens INTEGER, new_candidates INTEGER, triaged INTEGER,
  stop_reason TEXT, reported_msg INTEGER);
CREATE TABLE IF NOT EXISTS skill_trials (
  id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL REFERENCES scout_candidates(id),
  vendor_sha TEXT NOT NULL, task TEXT NOT NULL, condition TEXT NOT NULL,   -- with | without
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | running | passed | failed | timeout | over-budget | error | cancelled | voided
  agent TEXT, sandbox TEXT, queued TEXT NOT NULL, started TEXT, ended TEXT,
  passed INTEGER, wall_s INTEGER, tokens_in INTEGER, tokens_out INTEGER, cache_read INTEGER, cache_write INTEGER,
  skill_invoked INTEGER, check_output TEXT, author TEXT,
  void_reason TEXT, void_why TEXT,   -- set by `skilleval void`; the row keeps every other field
  UNIQUE(candidate_id, vendor_sha, task, condition));
