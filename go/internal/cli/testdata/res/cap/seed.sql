INSERT OR REPLACE INTO config(key,value) VALUES('min_mem_available_mb','1'),('terminal_backend','herdr'),('max_live_agents','2');
INSERT INTO agents(name,role,status,pane_id,started,session_id) VALUES ('claude-coder-a','coder','live','p1','2026-11-11T00:00:00+00:00','s1'),('claude-coder-b','coder','live','p2','2026-11-11T00:00:00+00:00','s2');
