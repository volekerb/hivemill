INSERT OR REPLACE INTO config(key,value) VALUES('min_mem_available_mb','1'),('terminal_backend','herdr');
INSERT INTO throttle(key,ts) VALUES('owner-blocker:gate','2026-11-12T00:00:00+00:00');
INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES ('2026-11-11T20:00:00+00:00','tick','tmp','-','tmp-swept','{"mb": 512, "dirs": 3}');
