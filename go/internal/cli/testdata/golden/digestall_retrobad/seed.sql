INSERT INTO retro_reports(day, causes, evidence, fixes, author, created) VALUES
 ('2026-11-12', '{not json', '[]', '[]', 'retro', '2026-11-12T10:00:00+00:00');
INSERT INTO scout_candidates(id, key, kind, repo, first_seen, last_seen) VALUES ('C-1', 'gh:o/x', 'skill', 'o/x', '2026-11-01T00:00:00+00:00', '2026-11-12T00:00:00+00:00');
INSERT INTO scout_triage(candidate_id, purpose, target_roles, overlap, recommendation, rationale, created) VALUES
 ('C-1', 'p', 'qa', 'none', 'adopt', 'outside the window', '2026-10-01T07:00:00+00:00');
INSERT INTO agents(name, role, status, goal_id, team_id, task_id, started, ended, flagless, flagless_since) VALUES
 ('claude-hq-flagless', 'director', 'live', NULL, NULL, NULL, '2026-11-12T09:00:00+00:00', NULL, 1, NULL);
