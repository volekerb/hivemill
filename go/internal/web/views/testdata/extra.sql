-- Rows the view goldens need on top of ../../data/testdata/fixture.sql (T-1475): plan versions for the goal detail page.
CREATE TABLE IF NOT EXISTS plans (
  id INTEGER PRIMARY KEY AUTOINCREMENT, goal_id TEXT NOT NULL REFERENCES goals(id),
  version INTEGER NOT NULL, body TEXT NOT NULL, author TEXT, created TEXT NOT NULL);
INSERT INTO plans(goal_id, version, body, author, created) VALUES
 ('G-001', 1, 'plan body one must never be rendered', 'claude-pm-g-001', '2026-11-01T08:30:00+01:00'),
 ('G-001', 2, 'plan body two must never be rendered', 'claude-pm-g-001', '2026-11-03T08:30:00+01:00'),
 ('G-002', 1, 'cursor plan', 'cursor-pm-g-002', '2026-11-05T08:30:00+01:00');
