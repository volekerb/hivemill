INSERT INTO goals(id, project_id, title, intent, status, priority, created, updated, org) VALUES
 ('G-1', NULL, 'Alpha goal', 'i', 'active', 2, '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00', 'claude'),
 ('G-2', NULL, 'Beta goal with a title that is far longer than sixty characters so the table cuts it off', 'i', 'active', 1, '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00', 'cursor'),
 ('G-3', NULL, 'Finished goal', 'i', 'done', 2, '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00', 'claude'),
 ('G-4', NULL, 'Dropped goal', 'i', 'cancelled', 3, '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00', 'cursor'),
 ('G-5', NULL, 'Proposed goal', 'i', 'proposed', 2, '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00', 'claude');
INSERT INTO tasks(id, goal_id, title, role, status, assignee, depends_on, pr_url, attempts, waits, created, updated) VALUES
 ('T-1', 'G-1', 'First task, done', 'coder', 'done', 'claude-coder-t-1', '[]', 'https://example.test/pr/1', 1, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-2', 'G-1', 'Second task in progress', 'coder', 'in_progress', 'claude-coder-t-2', '["T-1"]', NULL, 2, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-3', 'G-1', 'Blocked on the owner', 'pm', 'blocked', NULL, '[]', NULL, 0, 'owner', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-4', 'G-2', 'Blocked internally', 'coder', 'blocked', NULL, '[]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-5', 'G-2', 'Todo with deps met', 'qa', 'todo', NULL, '["T-1"]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-6', 'G-2', 'Todo with deps unmet', 'coder', 'todo', NULL, '["T-1","T-2"]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-7', 'G-1', 'Todo no deps', 'reviewer', 'todo', NULL, '[]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-8', 'G-1', 'Cancelled task', 'coder', 'cancelled', NULL, '[]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-9', 'G-2', 'A very long task title that runs well past the sixty character limit of the table cell', 'coder', 'in_review', 'claude-coder-t-9', '[]', 'https://example.test/pr/9', 1, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00'),
 ('T-10', 'G-3', 'Zażółć gęślą jaźń — ünïcödé 日本語', 'coder', 'todo', NULL, '[]', NULL, 0, 'internal', '2026-11-01T00:00:00+01:00', '2026-11-01T00:00:00+01:00');
