package hm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The guard export of a standalone home. The Go worker guard decides the coder's push pin, own goal, test sandbox and
// the public-text config from <home>/run/guard-export.json and fails closed when the file is missing or older than
// 15 minutes (guard.GuardExportMaxAge). In a factory checkout the Python tick writes it (bin/factory_cli.py
// write_guard_export); a standalone home has no tick, so every runner pass writes it here, with the same layout.

// guardExportKeys are GUARD_EXPORT_KEYS of bin/factory_cli.py with their DEFAULT_CONFIG values.
var guardExportKeys = map[string]string{"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "", "test_sandbox": "0",
	"factory_remote_url": ""}

// pyTickOwnsExport: the home is a factory checkout whose Python tick writes the export; the runner leaves it alone.
func pyTickOwnsExport(home string) bool {
	_, err := os.Stat(filepath.Join(home, "bin", "factory_cli.py"))
	return err == nil
}

// GuardExportDoc builds the export document from the factory DB.
func (s *SQLite) GuardExportDoc(now time.Time) (map[string]any, error) {
	cfg := map[string]string{}
	for k, def := range guardExportKeys {
		var v string
		if err := s.db.QueryRow(`SELECT value FROM config WHERE key=?`, k).Scan(&v); err != nil {
			v = def
		}
		cfg[k] = v
	}
	pairs := func(q string) (map[string]string, error) {
		rows, err := s.db.Query(q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, rows.Err()
	}
	goals, err := pairs(`SELECT name, goal_id FROM agents WHERE goal_id IS NOT NULL AND goal_id != ''`)
	if err != nil {
		return nil, err
	}
	wts, err := pairs(`SELECT id, worktree FROM tasks WHERE worktree IS NOT NULL AND worktree != ''`)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": 1, "generated_at": now.UTC().Format("2006-01-02T15:04:05+00:00"), "config": cfg,
		"agent_goals": goals, "task_worktrees": wts}, nil
}

// WriteGuardExport writes <home>/run/guard-export.json (mode 0600, atomic rename) unless the Python tick owns it, and
// gives the home its ci/ files (EnsureHomeFiles).
func (r *Runner) WriteGuardExport(now time.Time) error {
	if pyTickOwnsExport(r.Env.Home) {
		return nil
	}
	s, ok := r.Store.(*SQLite)
	if !ok {
		return nil
	}
	if _, err := EnsureHomeFiles(r.Env.Home); err != nil { // a home made by an older `hivemill init`
		return err
	}
	doc, err := s.GuardExportDoc(now)
	if err != nil {
		return fmt.Errorf("guard export: %w", err)
	}
	b, err := json.Marshal(doc) // map keys sorted, as sort_keys=True
	if err != nil {
		return err
	}
	dir := filepath.Join(r.Env.Home, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "guard-export.json")
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
