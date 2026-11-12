INSERT OR REPLACE INTO config(key, value) VALUES ('orgs_enabled', 'claude,cursor'),
 ('drift_pr_states', '{"https://github.com/o/r/pull/77": "MERGED", "https://github.com/o/r/pull/78/": "CLOSED", "https://github.com/o/r/pull/79": "OPEN"}');
INSERT INTO goals(id, project_id, title, intent, status, priority, created, updated, org) VALUES
 ('G-1', NULL, 'Active goal', 'i', 'active', 1, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'claude'),
 ('G-2', NULL, 'Planning goal', 'i', 'planning', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'claude'),
 ('G-3', NULL, 'Drift goal', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'claude'),
 ('G-4', NULL, 'Closed today', 'i', 'done', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T01:00:00+00:00', 'claude'),
 ('G-5', NULL, 'Closed long ago', 'i', 'done', 2, '2026-10-01T00:00:00+00:00', '2026-10-02T00:00:00+00:00', 'claude'),
 ('G-6', NULL, 'Cancelled quietly', 'i', 'cancelled', 2, '2026-10-01T00:00:00+00:00', '2026-11-01T00:00:00+00:00', 'claude'),
 ('G-7', NULL, 'Cursor goal', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'cursor'),
 ('G-8', NULL, 'Zürich — überwachung ✓ | pipe', 'i', 'active', 2, '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00', 'claude'),
 ('G-9', NULL, 'Closed, unparseable time', 'i', 'done', 2, '2026-10-01T00:00:00+00:00', 'garbage', 'claude');
INSERT INTO teams(id, goal_id, scope, lead_agent, status, created, parked_at) VALUES
 ('TA', 'G-1', NULL, 'claude-lead-a', 'active', '2026-10-01T00:00:00+00:00', NULL),
 ('TB', 'G-1', NULL, NULL, 'active', '2026-10-01T00:00:00+00:00', '2026-11-10T08:30:00+00:00'),
 ('TG2', 'G-2', NULL, NULL, 'active', '2026-10-01T00:00:00+00:00', NULL),
 ('TG3', 'G-3', NULL, NULL, 'closed', '2026-10-01T00:00:00+00:00', '2026-11-01T00:00:00+00:00'),
 ('ÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜ', 'G-8', NULL, NULL, 'active', '2026-10-01T00:00:00+00:00', NULL);
INSERT INTO tasks(id, goal_id, team_id, title, role, status, assignee, pr_url, depends_on, attempts, waits, created, updated) VALUES
 ('T-11', 'G-1', 'TA', 'merged in window', 'coder', 'done', NULL, 'https://github.com/o/r/pull/11', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-12', 'G-1', 'TA', 'merge verdict in window', 'coder', 'done', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-13', 'G-1', 'TA', 'closed by factory done', 'coder', 'done', NULL, '', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T08:00:00+00:00'),
 ('T-14', 'G-1', 'TA', 'building', 'coder', 'in_progress', 'claude-coder-14', NULL, '[]', 2, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-15', 'G-1', 'TA', 'in review', 'coder', 'in_review', NULL, 'https://github.com/o/r/pull/79', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-16', 'G-1', 'TA', 'blocked on owner', 'coder', 'blocked', NULL, NULL, '[]', 1, 'owner', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-17', 'G-1', 'TA', 'blocked internal', 'coder', 'blocked', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-18', 'G-1', 'TA', 'ready todo', 'coder', 'todo', NULL, NULL, '[]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T09:00:00+00:00'),
 ('T-19', 'G-1', 'TA', 'waiting todo', 'coder', 'todo', NULL, NULL, '["T-14"]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-20', 'G-1', 'TA', 'judge blocked', 'coder', 'in_review', 'claude-judge', NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-21', 'G-1', 'TA', 'blocked then merged', 'coder', 'in_review', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-22', 'G-1', 'TA', 'merge failed', 'coder', 'blocked', NULL, NULL, '[]', 1, 'owner', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-23', 'G-1', 'TA', 'blocked without a reason', 'coder', 'blocked', NULL, NULL, 'null', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-31', 'G-2', 'TG2', 'old done', 'coder', 'done', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-32', 'G-2', 'TG2', 'pr merged', 'coder', 'in_review', NULL, 'https://github.com/o/r/pull/77', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-33', 'G-2', 'TG2', 'pr closed', 'coder', 'in_review', NULL, 'https://github.com/o/r/pull/78/', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-34', 'G-2', 'TG2', 'pr unknown', 'coder', 'in_review', NULL, 'https://github.com/o/r/pull/80', '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-41', 'G-3', NULL, 'cancelled', 'coder', 'cancelled', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-42', 'G-3', NULL, 'depends on cancelled', 'coder', 'todo', NULL, NULL, '["T-41", "T-40"]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-43', 'G-3', NULL, 'idle building', 'coder', 'in_progress', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-44', 'G-3', NULL, 'old with live agent', 'coder', 'in_progress', 'claude-coder-44', NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-45', 'G-3', NULL, 'old waiting', 'coder', 'todo', NULL, NULL, '["T-43"]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-46', 'G-3', NULL, 'blocked long', 'coder', 'blocked', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-10T00:00:00+00:00'),
 ('T-47', 'G-3', NULL, 'blocked recently', 'coder', 'blocked', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T10:00:00+00:00'),
 ('T-48', 'G-3', NULL, 'old with queued agent', 'coder', 'todo', NULL, NULL, '[]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-10-05T00:00:00+00:00'),
 ('T-71', 'G-7', NULL, 'cursor task', 'coder', 'todo', NULL, NULL, '[]', 0, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T11:00:00+00:00'),
 ('T-81', 'G-8', 'ÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜÜ', 'Übergabe — |x| ✓', 'coder', 'done', NULL, NULL, '[]', 1, 'internal', '2026-10-01T00:00:00+00:00', '2026-11-12T10:00:00+00:00');
INSERT INTO agents(name, role, status, goal_id, team_id, task_id, started, ended, flagless, flagless_since) VALUES
 ('claude-lead-a', 'lead', 'live', 'G-1', 'TA', NULL, '2026-11-01T00:00:00+00:00', NULL, 0, NULL),
 ('claude-coder-14', 'coder', 'live', 'G-1', 'TA', 'T-14', '2026-11-12T10:00:00+00:00', NULL, 0, NULL),
 ('claude-lost-1', 'coder', 'lost', 'G-1', 'TA', 'T-14', '2026-11-12T09:00:00+00:00', '2026-11-12T09:30:00+00:00', 0, NULL),
 ('claude-lost-2', 'coder', 'lost', 'G-1', 'TA', 'T-11', '2026-11-12T09:00:00+00:00', '2026-11-12T09:30:00+00:00', 0, NULL),
 ('claude-lost-3', 'coder', 'lost', 'G-1', 'TA', NULL, '2026-11-12T09:00:00+00:00', '2026-11-12T09:30:00+00:00', 0, NULL),
 ('claude-flagless', 'coder', 'live', 'G-1', 'TA', NULL, '2026-11-12T09:00:00+00:00', NULL, 1, '2026-11-12T09:00:00+00:00'),
 ('claude-hq-flagless', 'director', 'live', NULL, NULL, NULL, '2026-11-12T09:00:00+00:00', NULL, 1, '2026-11-12T09:00:00+00:00'),
 ('claude-coder-44', 'coder', 'live', 'G-3', NULL, 'T-44', '2026-10-05T00:00:00+00:00', NULL, 0, NULL),
 ('claude-queued-48', 'coder', 'queued', 'G-3', NULL, 'T-48', NULL, NULL, 0, NULL);
INSERT INTO verdicts(task_id, role, verdict, head_sha, created) VALUES
 ('T-12', 'judge', 'merge', 'deadbeefcafe1234', '2026-11-12T06:00:00+00:00'),
 ('T-21', 'judge', 'merge', 'feedface', '2026-11-12T10:00:00+00:00'),
 ('T-11', 'judge', 'merge', 'old00000', '2026-10-05T00:00:00+00:00');
INSERT INTO evaluations(team_id, score, notes, author, created) VALUES
 ('TA', 3, NULL, 'x', '2026-10-01T00:00:00+00:00'), ('TA', 4, NULL, 'x', '2026-10-01T00:00:00+00:00'),
 ('TB', 5, NULL, 'x', '2026-10-01T00:00:00+00:00');
INSERT INTO throttle(key, ts) VALUES ('fresh:claude-coder-14', 'bin,roles'), ('fresh:claude-boss', 'roles'), ('other', '1');
INSERT INTO events(ts, actor, entity, entity_id, action, detail) VALUES
 ('2026-11-12T07:00:00+00:00', 'claude-judge', 'task', 'T-11', 'merged', '@abc1234'),
 ('2026-11-12T07:30:00+00:00', 'claude-coder-14', 'task', 'T-14', 'update', 'status=done'),
 ('2026-11-12T07:45:00+00:00', 'claude-judge', 'task', 'T-99', 'merged', '@other-goal'),
 ('2026-10-01T07:00:00+00:00', 'claude-lead-a', 'task', 'T-16', 'note', 'old note, outside the window'),
 ('2026-10-02T07:00:00+00:00', 'claude-lead-a', 'task', 'T-16', 'note', '   '),
 ('2026-10-03T07:00:00+00:00', 'claude-lead-a', 'task', 'T-16', 'note', '  needs   owner
 answer  '),
 ('2026-10-03T08:00:00+00:00', 'claude-lead-a', 'task', 'T-17', 'update', 'status=blocked — waiting on  X'),
 ('2026-11-12T08:00:00+00:00', 'claude-judge', 'task', 'T-20', 'judge-blocked', 'CI red on the PR'),
 ('2026-11-12T08:30:00+00:00', 'claude-judge', 'task', 'T-21', 'judge-blocked', 'superseded by the later merge'),
 ('2026-11-12T08:40:00+00:00', 'claude-judge', 'task', 'T-22', 'merge-failed', 'conflict'),
 ('2026-11-12T08:50:00+00:00', 'tick', 'agent', 'claude-lost-1', 'lost', 'gone'),
 ('2026-11-12T08:51:00+00:00', 'tick', 'agent', 'claude-lost-2', 'lost', 'gone'),
 ('2026-11-12T08:52:00+00:00', 'tick', 'agent', 'claude-lost-3', 'lost', 'gone'),
 ('2026-11-12T09:10:00+00:00', 'tick', 'agent', 'claude-nobody', 'lost', 'not in goal'),
 ('2026-11-12T09:20:00+00:00', 'tick', 'team', 'TB', 'lead-wake', 'x'),
 ('2026-11-12T09:30:00+00:00', 'tick', 'team', 'TB', 'lead-wake', 'x'),
 ('2026-10-12T09:30:00+00:00', 'tick', 'team', 'TB', 'lead-wake', 'before the window'),
 ('2026-11-12T01:00:00+00:00', 'claude-director', 'goal', 'G-4', 'status', 'done — merged'),
 ('2026-10-01T01:00:00+00:00', 'claude-director', 'goal', 'G-5', 'status', 'cancelled'),
 ('2026-10-02T01:00:00+00:00', 'claude-director', 'goal', 'G-5', 'status', 'done — later'),
 ('2026-10-02T01:00:00+00:00', 'claude-director', 'goal', 'G-5', 'status', 'active again'),
 ('2026-11-12T10:30:00+00:00', 'claude-director', 'task', NULL, 'update', 'no entity id'),
 ('2026-11-12T10:35:00+00:00', 'claude-director', 'task', 'T-81', 'merged', '@@x@');
