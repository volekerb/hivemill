package hm

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"factory/go/internal/citools/risktier"
)

// Tier rules of a run (the gate's tier and the merge decision). Without rules the tier cannot be decided and every
// merge is held for the owner, so a standalone home falls back to the built-in copy of the repo's ci/tier_rules.json.
// Order, first found wins: the project's own rules file (`hivemill project profile ID --tier-rules FILE`), the home's
// ci/tier_rules.json, the built-in copy. The project's sensitive_paths (`--sensitive GLOB`), when set, then replace
// the sensitive_paths of whichever file was used. A file that exists but cannot be loaded is an error (the tier
// stays undecided), never a silent fallback.

// ProjectProfile is the runner's per-project settings (table hm_project).
type ProjectProfile struct {
	ID             string
	SensitivePaths []string // replaces sensitive_paths of the tier rules; nil = keep the file's
	TierRules      string   // absolute path of the project's tier rules file; "" = the home's or the built-in one
	PostMergeCmd   string   // run in the project checkout after each merge (postmerge.go); "" = none
	PostMergeSecs  int      // its timeout; 0 = 900
}

// Profile reads a project's profile (an empty one when it has none).
func (s *SQLite) Profile(id string) (ProjectProfile, error) {
	p := ProjectProfile{ID: id}
	var sp, tr string
	err := s.db.QueryRow(`SELECT sensitive_paths, tier_rules, post_merge_cmd, post_merge_timeout_s
		FROM hm_project WHERE id=?`, id).Scan(&sp, &tr, &p.PostMergeCmd, &p.PostMergeSecs)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.SensitivePaths, p.TierRules = csv(sp), tr
	return p, nil
}

// SetProfile writes a project's profile.
func (s *SQLite) SetProfile(p ProjectProfile) error {
	_, err := s.db.Exec(`INSERT INTO hm_project(id,sensitive_paths,tier_rules,post_merge_cmd,post_merge_timeout_s) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET sensitive_paths=excluded.sensitive_paths, tier_rules=excluded.tier_rules,
		post_merge_cmd=excluded.post_merge_cmd, post_merge_timeout_s=excluded.post_merge_timeout_s`,
		p.ID, strings.Join(p.SensitivePaths, ","), p.TierRules, p.PostMergeCmd, p.PostMergeSecs)
	return err
}

// TierRules resolves the rules for a project; the second value says where they came from.
func (r *Runner) TierRules(project string) (risktier.Rules, string, error) {
	var prof ProjectProfile
	if s, ok := r.Store.(*SQLite); ok && project != "" {
		var err error
		if prof, err = s.Profile(project); err != nil {
			return risktier.Rules{}, "", err
		}
	}
	src := prof.TierRules
	if src == "" {
		src = filepath.Join(r.Env.Home, "ci", "tier_rules.json")
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			src = ""
		}
	}
	var rules risktier.Rules
	var err error
	from := src
	if src != "" {
		rules, err = risktier.LoadRules(src)
	} else {
		from = "built-in ci/tier_rules.json"
		rules, err = r.builtinTierRules()
	}
	if err != nil {
		return rules, from, err
	}
	if prof.SensitivePaths != nil {
		rules.SensitivePaths = prof.SensitivePaths
		from += " with the project's sensitive_paths"
	}
	return rules, from, nil
}

// builtinTierRules loads the built-in rules through a copy under the state dir (risktier reads a file).
func (r *Runner) builtinTierRules() (risktier.Rules, error) {
	b, err := Asset("tier_rules.json")
	if err != nil {
		return risktier.Rules{}, err
	}
	dir := r.Env.StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return risktier.Rules{}, err
	}
	p := filepath.Join(dir, "tier_rules.builtin.json")
	if cur, err := os.ReadFile(p); err != nil || string(cur) != string(b) {
		if err := writeFile(p, string(b), 0o600); err != nil {
			return risktier.Rules{}, fmt.Errorf("built-in tier rules: %w", err)
		}
	}
	return risktier.LoadRules(p)
}

// HomeAllowlist is the public-text allowlist a standalone home starts with: no entries, so no hit is allowlisted (the
// repo's own ci/scrub_allowlist.txt names files of the hivemill repo only).
const HomeAllowlist = "# Public-text allowlist of this hivemill home: reviewed, intentional generic-scan hits (never owner terms).\n" +
	"# Format: path:pattern-id:regex-of-line  # reason   (see docs/scrub-scan.md of the hivemill repo). Empty = none.\n"

// EnsureHomeFiles gives a standalone home the files that the guard, the pre-push hook and the gate read from
// <home>/ci: the public-text allowlist (HomeAllowlist). A file the home already has is never touched; a repo checkout
// has its own.
func EnsureHomeFiles(home string) ([]string, error) {
	p := filepath.Join(home, "ci", "scrub_allowlist.txt")
	if _, err := os.Stat(p); err == nil {
		return nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := writeFile(p, HomeAllowlist, 0o644); err != nil {
		return nil, err
	}
	return []string{"created " + p}, nil
}
