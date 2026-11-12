INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started)
VALUES
 ('lead-a', 'lead', 'G-9', 'TM-1', NULL, 'live', '2026-11-01T09:00:00+01:00'),
 ('coder-a', 'coder', 'G-9', NULL, 'T-9', 'live', '2026-11-01T10:00:00+01:00'),
 ('qa-a', 'qa', 'G-9', NULL, 'T-9', 'live', '2026-11-01T10:30:00+01:00'),
 ('dir-a', 'director', NULL, NULL, NULL, 'live', '2026-11-01T08:00:00+01:00'),
 ('boss', 'boss', NULL, NULL, NULL, 'live', '2026-11-01T07:00:00+01:00');
INSERT INTO messages(id, sender, recipient, kind, subject, body, goal_id, task_id, created, read_at) VALUES
 (1, 'dir-a', 'lead-a', 'report', 'status: G-9 weekly', 'line one' || char(10) || 'line two' || char(10) || 'line three' || char(10) || 'line four', 'G-9', NULL, '2026-11-02T10:00:00+01:00', NULL),
 (2, 'dir-a', 'lead-a', 'report', 'status: long single line', 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', NULL, 'T-9', '2026-11-02T10:01:00+01:00', NULL),
 (3, 'dir-a', 'lead-a', 'report', 'Daily Digest 2026-11-02', 'a' || char(10) || 'b' || char(10) || 'c' || char(10) || 'd' || char(10) || 'e', NULL, NULL, '2026-11-02T10:02:00+01:00', NULL),
 (4, 'coder-a', 'lead-a', 'request', 'question', 'short body', 'G-9', 'T-9', '2026-11-02T10:03:00+01:00', NULL),
 (5, 'coder-a', 'lead-a', 'msg', 'already read', 'old body', NULL, NULL, '2026-11-01T10:03:00+01:00', '2026-11-01T11:00:00+01:00'),
 (6, 'lead-a', 'coder-a', 'report', 'status: not collapsed for workers', 'l1' || char(10) || 'l2' || char(10) || 'l3' || char(10) || 'l4' || char(10) || 'l5', NULL, 'T-9', '2026-11-02T11:00:00+01:00', NULL),
 (7, 'lead-a', 'coder-a', 'msg', 'no body', NULL, NULL, NULL, '2026-11-02T11:01:00+01:00', NULL),
 (8, 'lead-a', 'coder-a', 'msg', 'odd lines ünï', 'first' || char(10) || char(10) || '   ' || char(10) || 'tab' || char(9) || 'x' || char(13) || 'cr line' || char(13) || char(10) || 'crlf line' || char(11) || 'vt' || char(12) || 'ff' || char(8232) || 'ls' || char(160) || char(10) || 'last' || char(10), NULL, NULL, '2026-11-02T11:02:00+01:00', NULL),
 (9, 'boss', 'dir-a', 'report', 'status: blank first line', char(10) || '   ' || char(10) || '  padded first  ' || char(10) || 'two' || char(10) || 'three' || char(10) || 'four', 'G-9', NULL, '2026-11-02T12:00:00+01:00', NULL),
 (10, 'dir-a', 'qa-a', 'msg', 'q1', 'b1', NULL, NULL, '2026-11-03T10:00:00+01:00', '2026-11-03T10:30:00+01:00'),
 (11, 'dir-a', 'qa-a', 'msg', 'q2', 'b2', NULL, NULL, '2026-11-03T10:01:00+01:00', NULL),
 (12, 'dir-a', 'qa-a', 'msg', 'q3', 'b3', NULL, NULL, '2026-11-03T10:02:00+01:00', NULL),
 (13, 'dir-a', 'qa-a', 'msg', 'q4', 'b4', NULL, NULL, '2026-11-03T10:03:00+01:00', NULL),
 (14, 'dir-a', 'qa-a', 'msg', 'q5', 'b5', NULL, NULL, '2026-11-03T10:04:00+01:00', NULL),
 (15, 'dir-a', 'boss', 'report', 'status: for the boss', 'r1' || char(10) || 'r2' || char(10) || 'r3' || char(10) || 'r4', 'G-9', NULL, '2026-11-03T11:00:00+01:00', NULL),
 (16, 'coder-a', 'lead-a', 'report', 'status: odd ünï subject', 'α' || char(10) || 'β' || char(10) || 'γ' || char(10) || 'δ', NULL, NULL, '2026-11-03T12:00:00+01:00', NULL);
