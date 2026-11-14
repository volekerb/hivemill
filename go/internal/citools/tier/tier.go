// Package tier classifies a PR diff as docs, normal or risky and picks how much of the suite runs: the Go port of
// ci/ci_tier.py (T-0658). Flags, output lines and exit codes are those of ci_tier.py; parity tests run both.
//
// ci/ci_tier.py runs in CI from the BASE commit, never from the PR, so a PR cannot loosen its own tier. The Go tool
// does the same, which means it has to be built from the base commit. Build command (from a checkout of the base
// commit, or an archive of it; the tool and the packages it imports are all stdlib-only):
//
//	git archive "$BASE_SHA" go/go.mod go/go.sum go/cmd/ci-tier go/internal/citools/affected go/internal/citools/tier \
//	  | tar -x -C "$RUNNER_TEMP/base"
//	(cd "$RUNNER_TEMP/base/go" && go build -o "$RUNNER_TEMP/ci-tier" ./cmd/ci-tier)
//	"$RUNNER_TEMP/ci-tier" classify --root . --base "$BASE_SHA" --head HEAD
//
// ci-tier needs python3 only when affected_smoke is set in the factory DB (see package affected); CI has no DB.
package tier

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"factory/go/internal/citools/affected"
)

const (
	MaxLines = 600
	Bin      = "bin/factory"
)

var riskyPaths = []string{
	"ci/public_text_guard.py", "ci/publish_repo.py", "ci/scrub_*", "ci/guard_*",
	"ci/ci_tier.py", "ci/affected.py", "ci/shard.py", "ci/test_count_gate.py", // the CI gates themselves
	"roles/hooks/*", "roles/*settings*",
	"scout/nsbox.py", "scout/gatebox.py", "scout/ghcred.py", "scout/egress.py", "scout/sandbox.py", "scout/workerbox.py",
	"systemd/*",
	".github/*",
}

var riskyDefs = map[string]bool{
	"cmd_judge": true, "verdict_carries": true, "head_has_main": true, "refresh_branch": true, "git_config_guard": true,
	"noreply_email": true, "_push_policy": true, "OWNER_CAP_KEYS": true, "DEFAULT_CONFIG": true, "SCHEMA": true,
	"_identity": true, "_state_push_refusal": true, "refuse_worker": true, "refuse_on_demand": true,
	"check_sandbox_roles": true, "_token_file_ok": true, "settings_tokens": true, "git_config_refusal": true,
	"_public_guard": true, "evaluator_home_guard": true, "verify_merged_externally": true,
}

var riskyDefPrefixes = []string{"judge_", "gate", "sandbox_"}

func isDocs(p string) bool {
	if strings.HasPrefix(p, "docs/") {
		return true
	}
	return strings.HasSuffix(p, ".md") && !strings.HasPrefix(p, "roles/")
}

func riskyPath(p string) bool {
	for _, pat := range riskyPaths {
		if affected.FnMatch(p, pat) {
			return true
		}
	}
	return false
}

func riskyDef(name string) bool {
	if riskyDefs[name] {
		return true
	}
	for _, pre := range riskyDefPrefixes {
		if strings.HasPrefix(name, pre) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

type numstat struct {
	add, del, path string
}

func readNumstat(root, base, head string) ([]numstat, error) {
	out, err := affected.Git(root, "diff", "--numstat", "--no-renames", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var rows []numstat
	if out == "" {
		return rows, nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			return nil, fmt.Errorf("malformed numstat line %q", line)
		}
		rows = append(rows, numstat{parts[0], parts[1], parts[2]})
	}
	return rows, nil
}

func changedLines(rows []numstat) int {
	total := 0
	for _, r := range rows {
		if isDigits(r.add) {
			n, _ := strconv.Atoi(r.add)
			total += n
		}
		if isDigits(r.del) {
			n, _ := strconv.Atoi(r.del)
			total += n
		}
	}
	return total
}

func newTopLevelRisk(root, base string, files []string) (string, error) {
	out, err := affected.Git(root, "ls-tree", "--name-only", base)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			known[l] = true
		}
	}
	for _, p := range files {
		top, _, _ := strings.Cut(p, "/")
		if !known[top] {
			return top, nil
		}
	}
	return "", nil
}

func binFactoryRisk(root, base, head string) (string, error) {
	var baseSrc, headSrc string
	var err error
	if affected.Exists(root, base, Bin) {
		if baseSrc, err = affected.Git(root, "show", base+":"+Bin); err != nil {
			return "", err
		}
	}
	if affected.Exists(root, head, Bin) {
		if headSrc, err = affected.Git(root, "show", head+":"+Bin); err != nil {
			return "", err
		}
	}
	names, err := affected.ChangedDefs(baseSrc, headSrc)
	if err != nil {
		if pe, ok := err.(*affected.ParseError); ok {
			return fmt.Sprintf("%s does not parse (%s)", Bin, pe.Msg), nil
		}
		return "", err
	}
	var hit []string
	for n := range names {
		if riskyDef(n) {
			hit = append(hit, n)
		}
	}
	if len(hit) == 0 {
		return "", nil
	}
	sort.Strings(hit)
	return fmt.Sprintf("%s changed in %s", hit[0], Bin), nil
}

// Classify returns (tier, reason) for the diff base...head.
func Classify(root, base, head string) (string, string, error) {
	cf, err := affected.ChangedFiles(root, base, head)
	if err != nil {
		return "", "", err
	}
	files := make([]string, len(cf))
	for i, c := range cf {
		files[i] = c.Path
	}
	if len(files) == 0 {
		return "normal", "empty diff", nil
	}
	rows, err := readNumstat(root, base, head)
	if err != nil {
		return "", "", err
	}
	var nonLine []string
	for _, r := range rows {
		if !(isDigits(r.add) && isDigits(r.del)) {
			nonLine = append(nonLine, r.path)
		}
	}
	if len(nonLine) > 0 {
		sort.Strings(nonLine)
		return "risky", "binary or non-text diff (numstat not line counts): " + nonLine[0], nil
	}
	for _, p := range files {
		if riskyPath(p) {
			return "risky", p + " is on the risky path list", nil
		}
	}
	top, err := newTopLevelRisk(root, base, files)
	if err != nil {
		return "", "", err
	}
	if top != "" {
		return "risky", "new top-level entry " + top, nil
	}
	for _, p := range files {
		if p == Bin {
			why, err := binFactoryRisk(root, base, head)
			if err != nil {
				return "", "", err
			}
			if why != "" {
				return "risky", why, nil
			}
			break
		}
	}
	n := changedLines(rows)
	if n > MaxLines {
		return "risky", fmt.Sprintf("%d changed lines (> %d)", n, MaxLines), nil
	}
	all := true
	for _, p := range files {
		if !isDocs(p) {
			all = false
			break
		}
	}
	if all {
		return "docs", "only docs files changed", nil
	}
	return "normal", fmt.Sprintf("%d files, %d changed lines", len(files), n), nil
}

// Result is the plan: tier, mode, go, reason and the affected modules (affected mode only).
type Result struct {
	Tier, Mode, Go, Reason string
	Modules                []string
}

// Fallback is the result for an undecidable diff: the whole suite.
func Fallback(reason string) Result {
	return Result{Tier: "risky", Mode: "full", Go: "true", Reason: reason}
}

// Plan is ci_tier.py plan.
func Plan(root, base, head string) (Result, error) {
	tier, reason, err := Classify(root, base, head)
	if err != nil {
		return Result{}, err
	}
	out := Result{Tier: tier, Reason: reason}
	cf, err := affected.ChangedFiles(root, base, head)
	if err != nil {
		return Result{}, err
	}
	switch tier {
	case "docs":
		out.Mode = "docs"
		testsDir := filepath.Join(root, "tests")
		mods, err := affected.AllModules(testsDir)
		if err != nil {
			return Result{}, err
		}
		hit := map[string]bool{}
		for _, c := range cf {
			if c.Path == "docs/README.md" {
				continue // the docs index test runs in docs mode anyway
			}
			for _, needle := range []string{c.Path, path.Base(c.Path)} {
				h, err := affected.Mentions(testsDir, mods, needle, false)
				if err != nil {
					return Result{}, err
				}
				for k := range h {
					hit[k] = true
				}
			}
		}
		if len(hit) > 0 {
			out.Mode = "affected"
			for k := range hit {
				out.Modules = append(out.Modules, k)
			}
			sort.Strings(out.Modules)
			out.Reason += "; tests mention the changed files"
		}
	case "risky":
		out.Mode = "full"
	default:
		sel, err := affected.Select(root, base, head, "")
		if err != nil {
			return Result{}, err
		}
		if sel.Full {
			out.Mode, out.Reason = "full", "affected.py: "+sel.Note
		} else {
			out.Mode, out.Modules = "affected", sel.Modules
		}
	}
	out.Go = "false"
	if out.Mode == "full" {
		out.Go = "true"
	}
	for _, c := range cf {
		if strings.HasPrefix(c.Path, "go/") {
			out.Go = "true"
		}
	}
	return out, nil
}

var (
	linkRe     = regexp.MustCompile(`\]\(([^)\s]+)`)
	schemeRe   = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*:|#|/)`)
	fenceStart = []string{"```", "~~~"}
)

// stripFences removes ``` or ~~~ blocks (an opening marker up to the next identical marker), like
// re.sub(r"(?s)(```|~~~).*?\1", "", text).
func stripFences(text string) string {
	var b strings.Builder
	pos := 0
	for {
		idx, marker := -1, ""
		for _, m := range fenceStart {
			if i := strings.Index(text[pos:], m); i >= 0 && (idx < 0 || i < idx) {
				idx, marker = i, m
			}
		}
		if idx < 0 {
			b.WriteString(text[pos:])
			return b.String()
		}
		idx += pos
		if c := strings.Index(text[idx+3:], marker); c >= 0 {
			b.WriteString(text[pos:idx])
			pos = idx + 3 + c + 3
			continue
		}
		b.WriteString(text[pos : idx+1])
		pos = idx + 1
	}
}

// DocsProblems lists problems in the changed markdown files: relative links that do not resolve, unbalanced fences.
func DocsProblems(root, base, head string) ([]string, error) {
	cf, err := affected.ChangedFiles(root, base, head)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, c := range cf {
		if !c.Exists || !strings.HasSuffix(c.Path, ".md") {
			continue
		}
		full := filepath.Join(root, c.Path)
		raw, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v; run from the head checkout", full, err)
		}
		text := string(raw)
		fences := 0
		for _, l := range affected.PySplitLines(text) {
			t := strings.TrimLeft(l, " \t\v\f")
			if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
				fences++
			}
		}
		if fences%2 == 1 {
			problems = append(problems, c.Path+": unbalanced code fence; close it")
		}
		for _, m := range linkRe.FindAllStringSubmatch(stripFences(text), -1) {
			target := m[1]
			if schemeRe.MatchString(target) {
				continue
			}
			rel, _, _ := strings.Cut(target, "#")
			if rel == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(full), rel)); err != nil {
				problems = append(problems, fmt.Sprintf("%s: link target %s does not exist; fix the path", c.Path, target))
			}
		}
	}
	return problems, nil
}
