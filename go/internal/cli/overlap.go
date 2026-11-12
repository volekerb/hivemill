package cli

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Planning-time file-overlap warning shared by the task write commands: the Go ports of SPEC_TOKEN, repo_tracked,
// spec_files and warn_file_overlap in bin/factory_cli.py (3377-3463). Frozen after merge: later tasks reuse, never edit.
// Everything that can make Go hand the command over (an unreadable depends_on, git output Python could not decode) is
// decided in planFileOverlap, before the caller's first write; applyFileOverlap only prints and writes.

const (
	overlapMaxLines = 10
	overlapGitWait  = 20 * time.Second
)

var overlapOpen = []string{"todo", "in_progress", "in_review", "blocked"}

// specWordRune is the class [\w.-] of SPEC_TOKEN; Python's \w is letters, numbers and "_".
func specWordRune(r rune) bool {
	return r == '.' || r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// specTokens is SPEC_TOKEN.findall(text) for (?<![\w.:/-])[\w.-]+(?:/[\w.-]+)*. A match is greedy and never needs
// backtracking, so a hand-written scan is exact; a match cannot start right after a character of the lookbehind set
// (which includes the last character of the previous match).
func specTokens(text string) []string {
	rs := []rune(text)
	var out []string
	for i := 0; i < len(rs); {
		behind := i > 0 && (specWordRune(rs[i-1]) || rs[i-1] == ':' || rs[i-1] == '/')
		if behind || !specWordRune(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && specWordRune(rs[j]) {
			j++
		}
		for j+1 < len(rs) && rs[j] == '/' && specWordRune(rs[j+1]) {
			j++
			for j < len(rs) && specWordRune(rs[j]) {
				j++
			}
		}
		out = append(out, string(rs[i:j]))
		i = j
	}
	return out
}

// pathParts are the components pathlib keeps: empty and "." components are dropped.
func pathParts(p string) []string {
	var parts []string
	for _, x := range strings.Split(p, "/") {
		if x != "" && x != "." {
			parts = append(parts, x)
		}
	}
	return parts
}

// pathParent is str(Path(p).parent) for a relative path.
func pathParent(p string) string {
	parts := pathParts(p)
	if len(parts) <= 1 {
		return "."
	}
	return strings.Join(parts[:len(parts)-1], "/")
}

// trackedSet is the tracked files of the project repo and the directories above them.
type trackedSet struct {
	files, dirs map[string]bool
}

func newTrackedSet(files []string) *trackedSet {
	t := &trackedSet{files: map[string]bool{}, dirs: map[string]bool{}}
	for _, f := range files {
		t.files[f] = true
		parts := pathParts(f)
		for n := len(parts) - 1; n >= 1; n-- {
			t.dirs[strings.Join(parts[:n], "/")] = true
		}
	}
	return t
}

// specFiles is spec_files: the repo-relative paths a spec names, either a tracked file or a new file (a name with an
// extension) in a tracked directory.
func specFiles(text string, t *trackedSet) map[string]bool {
	found := map[string]bool{}
	if text == "" || len(t.files) == 0 {
		return found
	}
	for _, tok := range specTokens(text) {
		for strings.HasPrefix(tok, "./") {
			tok = tok[2:]
		}
		tok = strings.TrimRight(tok, ".-")
		if !strings.Contains(tok, "/") && !strings.Contains(tok, ".") {
			continue
		}
		if t.files[tok] {
			found[tok] = true
			continue
		}
		last := tok[strings.LastIndex(tok, "/")+1:]
		_, size := utf8.DecodeRuneInString(last)
		if strings.Contains(tok, "/") && t.dirs[pathParent(tok)] && strings.Contains(last[size:], ".") &&
			!strings.HasSuffix(tok, "_") && !strings.HasSuffix(tok, "/") && !strings.HasSuffix(tok, "-") {
			found[tok] = true
		}
	}
	return found
}

// repoTracked is repo_tracked: `git ls-files` of the project repo with git_argv's hardening flags, run from the cwd;
// empty when there is no repo or git fails. git output that is not UTF-8 crashes Python's text mode: delegate.
func repoTracked(c *ctx, repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	live, err := c.env.HomeIsLive()
	if err != nil {
		return nil, errDelegate
	}
	proto := "always"
	if live {
		proto = "never"
	}
	argv := []string{"-C", repo}
	for _, kv := range [][2]string{{"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}, {"protocol.allow", "never"},
		{"protocol.https.allow", "always"}, {"protocol.file.allow", proto}, {"commit.gpgSign", "false"}, {"tag.gpgSign", "false"}} {
		argv = append(argv, "-c", kv[0]+"="+kv[1])
	}
	argv = append(argv, "ls-files")
	out, _, code, err := c.deps.runCmd("git", argv, "", overlayEnv(c.deps.Environ,
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"), overlapGitWait)
	if err != nil || code != 0 {
		return nil, nil // OSError, timeout or a failing git: no warning
	}
	if !utf8.ValidString(out) {
		return nil, errDelegate
	}
	return pySplitlines(pyNewlines(out)), nil
}

// overlayEnv is {**os.environ, k: v, ...}: env with each KEY=VALUE in set replacing an earlier entry of the same key.
func overlayEnv(env []string, set ...string) []string {
	skip := map[string]bool{}
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		skip[k] = true
	}
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !skip[k] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// fileOverlap is what warn_file_overlap decided: the stderr lines to print (already cut at overlapMaxLines, with the
// "... and N more" line) and the ids for the file-overlap event (none: no event).
type fileOverlap struct {
	lines []string
	ids   []string
}

// planFileOverlap is warn_file_overlap up to its first output: reads only. deps are the dependencies tid is about to
// get; tid is not stored yet.
func planFileOverlap(c *ctx, tid, goalID string, spec *string, deps []string) (*fileOverlap, error) {
	var repo sql.NullString
	err := c.db.QueryRow("SELECT p.repo_path FROM goals g JOIN projects p ON p.id=g.project_id WHERE g.id=?", goalID).Scan(&repo)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	files, err := repoTracked(c, repo.String)
	if err != nil {
		return nil, err
	}
	tracked := newTrackedSet(files)
	specText := ""
	if spec != nil {
		specText = *spec
	}
	mine := specFiles(specText, tracked)
	plan := &fileOverlap{}
	if len(mine) == 0 {
		return plan, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(overlapOpen)), ",")
	args := []any{tid}
	for _, s := range overlapOpen {
		args = append(args, s)
	}
	rs, err := c.query("SELECT id,goal_id,status,spec,depends_on FROM tasks WHERE id!=? AND status IN ("+ph+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	type other struct {
		id, goal, status string
		spec             sql.NullString
	}
	var others []other
	for rs.Next() {
		var o other
		var storedDeps sql.NullString // read through taskDepsOf, which parses it
		if err := rs.Scan(&o.id, &o.goal, &o.status, &o.spec, &storedDeps); err != nil {
			rs.Close()
			return nil, err
		}
		others = append(others, o)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	overlay := map[string][]string{tid: deps}
	var lines []string
	for _, o := range others {
		var both []string
		theirs := specFiles(o.spec.String, tracked)
		for f := range mine {
			if theirs[f] {
				both = append(both, f)
			}
		}
		if len(both) == 0 {
			continue
		}
		sort.Strings(both)
		names := strings.Join(both, ", ")
		if o.goal != goalID {
			lines = append(lines, fmt.Sprintf("note: %s (goal %s, %s) also names %s; name it in the spec and tell your lead (P3, L-020)",
				o.id, o.goal, o.status, names))
		} else {
			ordered, err := taskOrdered(c, tid, o.id, overlay)
			if err != nil {
				return nil, err
			}
			if ordered {
				continue
			}
			lines = append(lines, fmt.Sprintf("warning: %s (%s) also names %s; chain with --after %s or split the files (P3/P7)",
				o.id, o.status, names, o.id))
		}
		plan.ids = append(plan.ids, o.id)
	}
	if len(lines) == 0 {
		return plan, nil
	}
	plan.lines = lines
	if len(lines) > overlapMaxLines {
		plan.lines = append(append([]string{}, lines[:overlapMaxLines]...), fmt.Sprintf("... and %d more", len(lines)-overlapMaxLines))
	}
	return plan, nil
}

// applyFileOverlap prints the plan to stderr and records the file-overlap event, in Python's order.
func applyFileOverlap(c *ctx, tid string, p *fileOverlap) error {
	if len(p.ids) == 0 {
		return nil
	}
	for _, ln := range p.lines {
		fmt.Fprintln(c.errb, ln)
	}
	return event(c, "task", tid, "file-overlap", strp(strings.Join(p.ids, ",")))
}
