package hm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"factory/go/internal/guard"
)

// The factory config keys (table config) the runner, the guard export and the pre-push hook read. In a factory
// checkout `factory config` sets them; a standalone home sets them with `hivemill init --noreply-email/--scrub-terms`
// or `hivemill config KEY VALUE`, with the same checks. Nothing here reads ~/.config/factory.

// NoreplySuffix is the GitHub noreply domain every runner commit must use.
const NoreplySuffix = "@users.noreply.github.com"

// FactoryKeys are the factory config keys `hivemill config` reads and writes, with what each one is for.
var FactoryKeys = map[string]string{
	"noreply_email": "the commit identity of every session, checked by the gate and the merge (" +
		"`hivemill config noreply_email ID+USER" + NoreplySuffix + "`)",
	"scrub_terms_file": "the owner terms file (absolute path, kept outside every repo) the public-text checks need; " +
		"without it the gate's public-text check fails for a public_text_repos repo (`hivemill config scrub_terms_file /abs/path`)",
	"public_text_repos": "comma list of OWNER/NAME repos whose pushes and PR text the guard, the pre-push hook and the gate " +
		"check for owner terms (the gate checks every other repo for secrets only)",
	"frozen_repos":    "comma list of OWNER/NAME repos the runner never merges into",
	"required_checks": "comma list of CI check names every PR needs before a merge (a project's own list wins)",
}

// FactoryKeyNames returns the factory config keys, sorted.
func FactoryKeyNames() []string {
	out := make([]string, 0, len(FactoryKeys))
	for k := range FactoryKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CheckFactoryKey refuses a value bin/factory would refuse, and a terms file that cannot be read.
func CheckFactoryKey(key, value string) error {
	v := strings.TrimSpace(value)
	switch key {
	case "noreply_email":
		if v != "" && !strings.HasSuffix(strings.ToLower(v), NoreplySuffix) {
			return fmt.Errorf("noreply_email must end in %s (e.g. 123+handle%s), or be empty", NoreplySuffix, NoreplySuffix)
		}
	case "scrub_terms_file":
		if v == "" {
			return nil
		}
		if !filepath.IsAbs(v) {
			return fmt.Errorf("scrub_terms_file must be an absolute path, not %q", v)
		}
		fi, err := os.Stat(v)
		if err != nil {
			return fmt.Errorf("scrub_terms_file: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("scrub_terms_file %s is not a regular file", v)
		}
		f, err := os.Open(v)
		if err != nil {
			return fmt.Errorf("scrub_terms_file: %w", err)
		}
		f.Close()
	case "public_text_repos", "frozen_repos":
		for _, x := range strings.Split(v, ",") {
			if strings.TrimSpace(x) == "" {
				continue
			}
			if _, ok := guard.CIRepoSlug(x); !ok {
				return fmt.Errorf("%s: %q is not OWNER/NAME", key, strings.TrimSpace(x))
			}
		}
	case "required_checks":
	default:
		return fmt.Errorf("unknown factory key %s; known: %s", key, strings.Join(FactoryKeyNames(), ", "))
	}
	return nil
}

// SetFactoryKey checks and writes a factory config key.
func SetFactoryKey(s *SQLite, key, value string) error {
	if err := CheckFactoryKey(key, value); err != nil {
		return err
	}
	return s.SetFactoryCfg(key, strings.TrimSpace(value))
}

// GitGlobalEmail is `git config --global user.email` ("" when unset). Tests replace it.
var GitGlobalEmail = func() string {
	out, err := exec.Command("git", "config", "--global", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
