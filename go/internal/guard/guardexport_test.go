package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// export reads: a decision that needs the DB values exits 2 (a closed Delegate, never Python) when the export is
// missing, unparsable, of another version or stale; with no DB file nothing is needed.
func TestGuardExportFailsClosed(t *testing.T) {
	clearTermsEnv(t)
	t.Setenv("FACTORY_AGENT", "coder-t-9")
	good := `{"version":1,"generated_at":"` + time.Now().UTC().Format(time.RFC3339) + `","config":{},"agent_goals":{},"task_worktrees":{}}`
	for _, c := range []struct {
		name string
		raw  *string // nil: no file
		want string
	}{
		{"missing", nil, "does not exist"},
		{"empty", ptr(""), "not valid JSON"},
		{"unparsable", ptr("{not json"), "not valid JSON"},
		{"wrong version", ptr(strings.Replace(good, `"version":1`, `"version":2`, 1)), "unknown layout"},
		{"no maps", ptr(`{"version":1,"generated_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`), "unknown layout"},
		{"no generated_at", ptr(strings.Replace(good, "generated_at", "generated", 1)), "no valid generated_at"},
		{"stale", ptr(setStamp(good, "2001-01-01T00:00:00Z")), "is stale"},
		{"far future", ptr(setStamp(good, "2999-01-01T00:00:00Z")), "is stale"},
	} {
		home := dbHome(t)
		if c.raw == nil {
			os.Remove(filepath.Join(home, "run", "guard-export.json"))
			cachedExport.done = false
		} else {
			writeExportRaw(t, home, []byte(*c.raw))
		}
		t.Setenv("FACTORY_HOME", home)
		for _, cmd := range []string{"factory goal show G-005", "git push origin HEAD:factory/t-9", "uvx pytest"} {
			err := checkRole(cmd, "coder")
			if !IsClosed(err) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %q: %v, want a closed refusal containing %q", c.name, cmd, err, c.want)
			}
		}
		t.Setenv(termsVar, "") // a terms role without an exported path needs scrub_terms_file from the export too
		unsetEnv(t, termsVar)
		if err := checkRole("ls -la", "coder"); !IsClosed(err) {
			t.Errorf("%s: terms role: %v, want a closed refusal", c.name, err)
		}
		if err := Check("git commit -m x", "", "", true); !IsClosed(err) {
			t.Errorf("%s: hq git commit: %v, want a closed refusal", c.name, err)
		}
		// a command that reads none of these values never needs the export
		if err := checkRole("ls -la && factory task show T-9", "cleaner"); err != nil {
			t.Errorf("%s: cleaner ls: %v, want allow", c.name, err)
		}
	}
	// the age limit is the documented constant: just inside is fine
	home := dbHome(t)
	writeExport(t, home, exportDoc{Age: GuardExportMaxAge - time.Minute})
	t.Setenv("FACTORY_HOME", home)
	if err := checkRole("factory goal show G-005", "coder"); IsClosed(err) {
		t.Errorf("just inside the limit: %v", err)
	}
	writeExport(t, home, exportDoc{Age: GuardExportMaxAge + time.Minute})
	if err := checkRole("factory goal show G-005", "coder"); !IsClosed(err) {
		t.Errorf("just outside the limit: %v, want a closed refusal", err)
	}
	// no DB file: the hook's "no DB" branch, no export needed
	t.Setenv("FACTORY_HOME", t.TempDir())
	if err := checkRole("factory goal show G-005", "coder"); IsClosed(err) || IsDelegate(err) {
		t.Errorf("no DB file: %v", err)
	}
}

// setStamp replaces the generated_at value of a test export, whatever the current date.
func setStamp(doc, stamp string) string {
	return regexp.MustCompile(`"generated_at":"[^"]*"`).ReplaceAllString(doc, `"generated_at":"`+stamp+`"`)
}

func ptr(s string) *string { return &s }

// f1: the scrub_terms_file row of the export is honoured with the terms variable unset (Bash, Read and Grep paths).
func TestScrubTermsFileFromExport(t *testing.T) {
	dir := t.TempDir()
	terms := filepath.Join(dir, "owner-terms.txt")
	if err := os.WriteFile(terms, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := dbHome(t)
	writeExport(t, home, exportDoc{Config: map[string]string{"scrub_terms_file": terms}})
	t.Setenv("FACTORY_HOME", home)
	unsetEnv(t, termsVar) // dbHome exported a terms variable, as spawn does; f1 is the case without one
	for _, role := range []string{"coder", "qa", "reviewer", "judge", "pm"} {
		err := checkRole("cat "+terms, role)
		if err == nil || IsDelegate(err) {
			t.Errorf("%s: cat of the terms file: %v, want a block", role, err)
		}
		if err := checkRole("ls -la", role); err != nil {
			t.Errorf("%s: ls: %v, want allow", role, err)
		}
	}
	if err := checkRole("cat "+terms, "cleaner"); err != nil {
		t.Errorf("cleaner is not a terms role: %v", err)
	}
	code, msg, closed := TermsTool(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": terms}})
	if code != 2 || closed || msg == "" {
		t.Errorf("Read of the terms file: %d %q closed=%v, want a block", code, msg, closed)
	}
	code, _, closed = TermsTool(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/etc/hostname"}})
	if code != 0 || closed {
		t.Errorf("Read of another file: %d closed=%v, want allow", code, closed)
	}
	// a stale export makes the Read fail closed as well
	writeExport(t, home, exportDoc{Config: map[string]string{"scrub_terms_file": terms}, Age: 2 * GuardExportMaxAge})
	if _, _, closed := TermsTool(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": terms}}); !closed {
		t.Error("Read with a stale export did not fail closed")
	}
}

// git_push_pin from the export: the recorded worktree must hold the pane's cwd and the -C target.
func TestPushPinFromExport(t *testing.T) {
	clearTermsEnv(t)
	f := newGitFix(t)
	home := dbHome(t)
	writeExport(t, home, exportDoc{Worktrees: map[string]string{"T-7": f.wt}})
	t.Setenv("FACTORY_HOME", home)
	sub := filepath.Join(f.wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f.allowed(t, "git push origin HEAD:factory/t-7", f.wt, "coder")
	f.allowed(t, "git push origin HEAD:factory/t-7", sub, "coder")
	f.allowed(t, "git -C "+f.wt+" push origin HEAD:factory/t-7", f.wt, "coder")
	f.blocked(t, "git push origin HEAD:factory/t-7", f.home, "coder")             // another dir
	f.blocked(t, "git push origin HEAD:factory/t-7", f.wt+"-evil", "coder")       // prefix of the worktree is not inside it
	f.blocked(t, "git -C "+sub+" push origin HEAD:factory/t-7", f.wt, "coder")    // -C names a subdir, not the worktree
	f.blocked(t, "git -C "+f.home+" push origin HEAD:factory/t-7", f.wt, "coder") // -C names another repo
	f.blocked(t, "git push origin HEAD:factory/t-8", f.wt, "coder")
	t.Setenv("FACTORY_AGENT", "coder-t-6") // no worktree recorded for t-6
	f.blocked(t, "git push origin HEAD:factory/t-6", f.wt, "coder")
}
