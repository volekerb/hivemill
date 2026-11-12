INSERT INTO goals(id, project_id, title, intent, status, priority, created, updated, org) VALUES
 ('G-1', NULL, 'One', 'i', 'active', 1, '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00', 'claude'),
 ('G-2', NULL, 'Two', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00', 'claude'),
 ('G-3', NULL, 'Three', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00', 'claude'),
 ('G-4', NULL, 'Four', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00', 'claude'),
 ('G-5', NULL, 'Five', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00', 'claude');
INSERT INTO agents(name, role, status, goal_id, team_id) VALUES
 ('claude-lead-a', 'lead', 'live', 'G-1', 'TA'), ('claude-lead-c', 'lead', 'queued', 'G-3', 'TC'),
 ('claude-lead-e', 'lead', 'done', 'G-5', 'TE'), ('claude-lead-f', 'lead', 'starting', 'G-5', 'TF'),
 ('claude-director', 'director', 'live', NULL, NULL), ('claude-coder-1', 'coder', 'live', 'G-1', 'TA');
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created, parked_at) VALUES
 ('TA', 'G-1', NULL, 'claude-lead-a', 'active', '2026-10-01T00:00:00+00:00', NULL),
 ('TB', 'G-2', NULL, NULL, 'active', '2026-10-01T00:00:00+00:00', '2026-10-02T00:00:00+00:00'),
 ('TC', 'G-3', NULL, 'claude-lead-c', 'active', '2026-10-01T00:00:00+00:00', NULL),
 ('TD', 'G-4', NULL, NULL, 'closed', '2026-10-01T00:00:00+00:00', '2026-10-02T00:00:00+00:00'),
 ('TE', 'G-5', NULL, 'claude-lead-e', 'active', '2026-10-01T00:00:00+00:00', NULL),
 ('TF', 'G-5', NULL, 'claude-lead-f', 'active', '2026-10-01T00:00:00+00:00', NULL);
INSERT INTO tasks(id, goal_id, team_id, title, role, status, depends_on, attempts, waits, created, updated) VALUES
 ('T-1', 'G-1', 'TA', 'a1', 'coder', 'done', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00'),
 ('T-2', 'G-1', 'TA', 'a2', 'coder', 'in_review', '[]', 3, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00'),
 ('T-3', 'G-1', 'TA', 'a3', 'coder', 'todo', '[]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00'),
 ('T-4', 'G-2', NULL, 'b1 goal-wide', 'coder', 'done', '[]', 2, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00'),
 ('T-5', 'G-2', 'TB', 'b2', 'coder', 'done', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00'),
 ('T-6', 'G-3', NULL, 'c1 goal-wide', 'pm', 'done', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-01T00:00:00+00:00');
INSERT INTO verdicts(task_id, role, verdict, created) VALUES
 ('T-1', 'judge', 'merge', '2026-10-01T00:00:00+00:00'), ('T-2', 'reviewer', 'fail', '2026-10-01T00:00:00+00:00'),
 ('T-2', 'qa', 'reject', '2026-10-01T00:00:00+00:00'), ('T-2', 'qa', 'pass', '2026-10-01T00:00:00+00:00'),
 ('T-4', 'judge', 'merge', '2026-10-01T00:00:00+00:00'), ('T-5', 'judge', 'merge', '2026-10-01T00:00:00+00:00'),
 ('T-6', 'reviewer', 'reject', '2026-10-01T00:00:00+00:00');
INSERT INTO evaluations(team_id, score, notes, author, created) VALUES
 ('TA', 3, NULL, 'x', '2026-10-01T00:00:00+00:00'), ('TA', 2, NULL, 'x', '2026-10-01T00:00:00+00:00'),
 ('TB', 4, NULL, 'x', '2026-10-01T00:00:00+00:00'), ('TB', 5, NULL, 'x', '2026-10-01T00:00:00+00:00'),
 ('TC', 5, NULL, 'x', '2026-10-01T00:00:00+00:00'), ('TE', 1, NULL, 'x', '2026-10-01T00:00:00+00:00'),
 ('TE', 2, NULL, 'x', '2026-10-01T00:00:00+00:00'), ('TE', 2, NULL, 'x', '2026-10-01T00:00:00+00:00');
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-10-12T09:00:00+00:00', 'claude-director', 'config', 'max_live_agents', 'set', '6 -> 8'),
 ('2026-10-12T09:30:00+00:00', 'claude-boss-x', 'config', 'max_live_agents', 'set', 'not an agent row'),
 ('2026-10-12T09:40:00+00:00', 'owner', 'config', 'max_total_claude', 'set', 'owner'),
 ('2026-10-12T09:50:00+00:00', 'claude-director', 'config', 'test_slots', 'set', 'not a cap key'),
 ('2026-10-12T09:55:00+00:00', 'claude-director', 'config', 'tmp_max_pct', 'get', 'not a set'),
 ('2026-10-12T23:59:00+00:00', 'claude-lead-a', 'config', 'max_live_leads', 'set', 'a lead'),
 ('2026-10-13T00:00:00+00:00', 'claude-director', 'config', 'max_live_judges', 'set', 'at the window end, excluded'),
 ('2026-10-11T00:00:00+00:00', 'claude-director', 'config', 'max_live_judges', 'set', 'before a 1 day window');
INSERT INTO messages(sender, recipient, kind, subject, body, created) VALUES
 ('claude-boss', 'director', 'msg', 'director score 2026-10-01: 5/5', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'director', 'msg', 'director score 2026-10-11: 1/5', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'director', 'msg', 'director score 2026-10-12: 2/5', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'director', 'msg', 'director score 2026-10-12: 6/5', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'director', 'msg', 'director score 2026-12-01: 5/5', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'director', 'msg', 'other subject', '', '2026-10-01T00:00:00+00:00'),
 ('claude-boss', 'claude-pm', 'msg', 'director score 2026-10-12: 5/5', '', '2026-10-01T00:00:00+00:00');
INSERT INTO util_samples(ts, live, workers, leads, judges, hq, other, queued, queued_workers, cap, target, demand, p1_starved, p2_starved) VALUES
 ('2026-10-01T10:00:00+00:00', 8, 1, 0, 0, 0, 0, 0, 0, 6, 4, 0, '[]', '[]'),
 ('2026-10-05T10:00:00+00:00', 5, 2, 0, 0, 0, 0, 1, 0, 6, 4, 0, '["G-1"]', '[]'),
 ('2026-10-05T10:03:00+00:00', 5, 3, 0, 0, 0, 0, 1, 0, 6, 4, 0, '["G-1","G-2"]', '[]'),
 ('2026-10-05T10:20:00+00:00', 6, 3, 0, 0, 0, 0, 0, 0, 6, 5, 0, '["G-2"]', '[]'),
 ('2026-10-05T10:21:00+00:00', 6, 4, 0, 0, 0, 0, 2, 0, 6, 5, 0, '["G-2","G-1"]', '[]'),
 ('2026-10-05T07:59:00+00:00', 9, 9, 0, 0, 0, 0, 9, 0, 6, 9, 0, '["G-9"]', '[]'),
 ('2026-10-05T20:00:00+00:00', 9, 9, 0, 0, 0, 0, 9, 0, 6, 9, 0, '["G-9"]', '[]'),
 ('2026-10-08T09:00:00+00:00', 3, 1, 0, 0, 0, 0, 1, 0, 6, 2, 0, '["G-3"]', '[]'),
 ('2026-10-08T09:01:30+00:00', 3, 2, 0, 0, 0, 0, 1, 0, 6, 2, 0, '["G-3"]', '[]'),
 ('2026-10-08T09:04:00+00:00', 3, 2, 0, 0, 0, 0, 3, 0, 6, 2, 0, '["G-3","G-4"]', '[]'),
 ('2026-10-08T09:30:00+00:00', 3, 3, 0, 0, 0, 0, 0, 0, 6, 2, 0, '[]', '[]'),
 ('2026-10-10T09:00:00', 4, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, '', '[]'),
 ('2026-10-10T10:00:00+02:00', 4, 2, 0, 0, 0, 0, 0, 0, 6, 3, 0, 'not json', '[]'),
 ('2026-10-10T11:00:00Z', 4, 1, 0, 0, 0, 0, 1, 0, 6, 3, 0, '[]', '[]'),
 ('2026-10-10T12:00:00+00:00', 0, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, '[]', '[]'),
 ('garbage', 4, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, '[]', '[]'),
 ('2026-10-12T10:00:00+00:00', 4, 2, 0, 0, 0, 0, 0, 0, 6, 3, 0, '[]', '[]'),
 ('2026-10-15T07:30:00+00:00', 6, 3, 0, 0, 0, 0, 1, 0, 6, 4, 0, '["G-5"]', '[]'),
 ('2026-10-15T19:30:00+00:00', 6, 6, 0, 0, 0, 0, 1, 0, 6, 4, 0, '["G-5"]', '[]'),
 ('2026-10-11T10:00:00+00:00', 4, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, 'null', '[]'),
 ('2026-10-13T10:00:00+00:00', 4, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, '[7]', '[]'),
 ('2026-10-16T09:00:00+00:00', 3, 1, 0, 0, 0, 0, 1, 0, 6, 3, 0, '["G-1"]', '[]'),
 ('2026-10-16T09:02:30+00:00', 3, 2, 0, 0, 0, 0, 1, 0, 6, 3, 0, '[]', '[]');
