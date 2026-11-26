package hm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// exportMu serialises the writers of one process (the pass and the daemon's refresher share the temp file name).
var exportMu sync.Mutex

// GuardExportEvery is how often the daemon refreshes the export between passes.
var GuardExportEvery = time.Minute

// keepGuardExport rewrites the export every GuardExportEvery until ctx ends, so a pass stuck in a long step (a
// 15-minute proof re-run, a wait for a test slot) never leaves the other sessions with a stale export.
func (r *Runner) keepGuardExport(ctx context.Context) {
	t := time.NewTicker(GuardExportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := r.WriteGuardExport(now); err != nil {
				r.printf("guard export: %v\n", err)
			}
			r.pushGuardExports(now)
		}
	}
}

// The guard export of a factory home. The Go worker guard decides the coder's push pin, own goal, test sandbox and
// the public-text config from <home>/run/guard-export.json and fails closed when the file is missing or older than
// 15 minutes (guard.GuardExportMaxAge). The daemon writes it at start and every GuardExportEvery, every runner pass
// and session start writes it too, and `hivemill guard export` writes it on demand, with the layout of the old
// chain's write_guard_export.

// guardExportKeys are the old chain's GUARD_EXPORT_KEYS with their DEFAULT_CONFIG values.
var guardExportKeys = map[string]string{"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "", "test_sandbox": "0",
	"factory_remote_url": ""}

// GuardExportDoc builds the export document from the factory DB.
func (s *SQLite) GuardExportDoc(now time.Time) (map[string]any, error) {
	cfg := map[string]string{}
	for k, def := range guardExportKeys {
		var v string
		err := s.db.QueryRow(`SELECT value FROM config WHERE key=?`, k).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			v = def
		} else if err != nil { // never export a default in place of a value that could not be read
			return nil, fmt.Errorf("config %s: %w", k, err)
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
	var agents int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agents'`).Scan(&agents); err != nil {
		return nil, err
	}
	goals := map[string]string{}
	if agents > 0 {
		var err error
		if goals, err = pairs(`SELECT name, goal_id FROM agents WHERE goal_id IS NOT NULL AND goal_id != ''`); err != nil {
			return nil, err
		}
	}
	wts, err := pairs(`SELECT id, worktree FROM tasks WHERE worktree IS NOT NULL AND worktree != ''`)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": 1, "generated_at": now.UTC().Format("2006-01-02T15:04:05+00:00"), "config": cfg,
		"agent_goals": goals, "task_worktrees": wts}, nil
}

// WriteGuardExport writes <home>/run/guard-export.json (mode 0600, atomic rename) and gives the home its ci/ files
// (EnsureHomeFiles).
func (r *Runner) WriteGuardExport(now time.Time) error {
	exportMu.Lock()
	defer exportMu.Unlock()
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
