package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // the "sqlite" driver (read-only config read of the pre-push hook)

	"factory/go/internal/citools/affected"
	"factory/go/internal/citools/risktier"
	"factory/go/internal/guard"
	"factory/go/internal/hm"
)

// The commands a standalone home needs instead of the repo's scripts: `guard` (the worker guard hook), `check
// public-text` (the git pre-push hook, ci/pre_push_public_text.py) and `affected` (ci/affected.py). They run before the
// runner opens the DB and before the FACTORY_AGENT gate: a session's hooks and pushes call them.

// cmdGuard is `hivemill guard --home DIR (--role ROLE | --hq) ...`: the PreToolUse hook of a standalone home's
// sessions (guard.HookMain, the code of the Go worker-guard binary), with DIR added to the guard's live homes. Exit 0
// allows, 2 blocks; a bad call blocks.
func cmdGuard(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(stderr, "hivemill guard: internal error: %v; blocking\n", p)
			code = 2
		}
	}()
	if len(args) < 2 || args[0] != "--home" || !filepath.IsAbs(args[1]) {
		fmt.Fprintln(stderr, "hivemill guard: usage: hivemill guard --home /abs/home (--role ROLE | --hq); blocking")
		return 2
	}
	home, rest := filepath.Clean(args[1]), args[2:]
	// stdin is read once, for the release limit, and the guard gets the same bytes (one past its cap still blocks).
	raw, err := io.ReadAll(io.LimitReader(stdin, guard.MaxHookInput+1))
	if err != nil {
		fmt.Fprintln(stderr, "hivemill guard: reading stdin:", err, "; blocking")
		return 2
	}
	if releaseRefused(home, raw) {
		fmt.Fprintln(stderr, "hivemill guard: App Store release refused: the owner has not answered release build N for this build")
		return 2
	}
	stdin = bytes.NewReader(raw)
	guard.SetLiveHome(home)
	for _, a := range rest {
		if a == "--py-hook" {
			return guard.HookMain(rest, stdin, stdout, stderr)
		}
	}
	// The repo layout under the home: the checkout the hook belongs to is the home (its ci/scrub_allowlist.txt for a
	// push check); a home without roles/hooks/worker-guard.py has no Python hook, so a decision that needs it blocks.
	py := filepath.Join(home, "roles", "hooks", "worker-guard.py")
	return guard.HookMain(append([]string{"--py-hook", py}, rest...), stdin, stdout, stderr)
}

// releaseRefused is true for a Bash call that submits or releases to the App Store while the home's DB holds no owner
// answer for its build. A DB that cannot be read holds no answer. Any other payload is left to the guard.
func releaseRefused(home string, raw []byte) bool {
	var call struct {
		Tool  string `json:"tool_name"`
		Input struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(raw, &call) != nil || call.Tool != "Bash" {
		return false
	}
	submit, build := hm.ReleaseAction(call.Input.Command)
	if !submit {
		return false
	}
	asks, err := answeredAsks(home)
	return err != nil || !hm.ReleaseAllowed(asks, build)
}

// answeredAsks reads the answered owner questions of the home's factory DB, read-only.
func answeredAsks(home string) ([]hm.Ask, error) {
	con, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer con.Close()
	rows, err := con.Query("SELECT status, answered_by, answer FROM hm_asks WHERE status='answered'")
	if err != nil {
		return nil, fmt.Errorf("read asks: %w", err)
	}
	defer rows.Close()
	var out []hm.Ask
	for rows.Next() {
		var a hm.Ask
		if err := rows.Scan(&a.Status, &a.AnsweredBy, &a.Answer); err != nil {
			return nil, fmt.Errorf("scan asks: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read asks: %w", err)
	}
	return out, nil
}

// cmdCheck is `hivemill check public-text --home DIR REMOTE URL`: the git pre-push hook of a task clone (the Go port
// of ci/pre_push_public_text.py, same arguments, stdin and exit codes).
func cmdCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "public-text" {
		fmt.Fprintln(stderr, "usage: hivemill check public-text --home DIR REMOTE URL (git pre-push hook: pushed refs on stdin)")
		return 2
	}
	return guard.CIPrePushPublicTextMain(args[1:], stdin, stderr, prePushConfig)
}

// prePushConfig is pre_push_public_text.read_config: public_text_repos and scrub_terms_file of the home's factory
// DB, read-only.
func prePushConfig(home string) (map[string]string, error) {
	con, err := openHomeDB(home)
	if err != nil {
		return nil, err
	}
	defer con.Close()
	rows, err := con.Query("SELECT key, value FROM config WHERE key IN ('public_text_repos','scrub_terms_file')")
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan config: %w", err)
		}
		out[k] = v.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return out, nil
}

// openHomeDB opens the factory DB of a home (db/factory.db, else the legacy file) read-only.
func openHomeDB(home string) (*sql.DB, error) {
	db := filepath.Join(home, "db", "factory.db")
	if _, err := os.Stat(db); err != nil {
		legacy := filepath.Join(home, "factory.db")
		if _, lerr := os.Stat(legacy); lerr != nil {
			return nil, fmt.Errorf("factory db: %w", err)
		}
		db = legacy
	}
	dsn := "file:" + (&url.URL{Path: db}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(5000)"
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open factory db: %w", err)
	}
	return con, nil
}

// cmdAffected is `hivemill affected --base REF [--head REF] [--list|--run] [--quick] [--budget S] [--root DIR]`, the Go
// port of ci/affected.py (the tests it selects are the factory repo's Python tests, run with python3).
func cmdAffected(args []string, stdout, stderr io.Writer) int {
	return affected.Main(args, stdout, stderr)
}

// cmdProofs is `hivemill proofs touched`: the tests of every JS package changed since REF and, with --ios-dir, an
// xcodebuild of the app when iOS files changed; a project's gate proof. The test and build output goes to stderr, one
// line per proof to stdout.
func cmdProofs(args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill proofs touched --base REF [--root DIR] " +
		"[--ios-dir DIR --xcode-workspace PATH|--xcode-project PATH --xcode-scheme NAME]"
	if len(args) == 0 || args[0] != "touched" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := newFlags("proofs touched", stderr)
	base := fs.String("base", "", "the ref the branch is compared with")
	root := fs.String("root", ".", "the checkout")
	iosDir := fs.String("ios-dir", "", "the iOS app directory under the root; a change under it runs xcodebuild")
	workspace := fs.String("xcode-workspace", "", "the .xcworkspace, relative to --ios-dir")
	project := fs.String("xcode-project", "", "the .xcodeproj, relative to --ios-dir")
	scheme := fs.String("xcode-scheme", "", "the scheme to build")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || *base == "" || (*workspace != "" && *project != "") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	run := hm.TouchedRun{Root: *root, Base: *base, Out: stdout, IOSDir: *iosDir, Workspace: *workspace, Project: *project,
		Scheme: *scheme}
	run.Exec = func(dir string, argv []string) (int, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stderr, stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return run.Run()
}

// cmdPR is `hivemill pr create --title TITLE --body - | pr ready` for a task session.
func cmdPR(r *hm.Runner, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := "usage: hivemill pr create --title 'T-1234: what the change does' --body - (body on stdin) | pr ready"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	action, title, body := args[0], "", ""
	switch action {
	case "create":
		fs := newFlags("pr create", stderr)
		t := fs.String("title", "", "PR title: the task id, a colon and what the change does")
		b := fs.String("body", "", "`-`: read the body from stdin")
		if fs.Parse(args[1:]) != nil || fs.NArg() > 0 || *b != "-" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return fail(stderr, err)
		}
		title, body = *t, string(raw)
	case "ready":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "`hivemill pr ready` takes no arguments: it targets the PR of your own branch")
			return 2
		}
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	wd, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	out, err := r.PR(action, title, body, wd)
	fmt.Fprint(stdout, out)
	if errors.Is(err, hm.ErrUsage) {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 2
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

// cmdProfile is `hivemill project profile ID [--sensitive GLOB]... [--tier-rules FILE] [--release-cmd CMD] [--clear]`: the
// project's own tier settings and release command (print them when no flag is given). --clear is for the tier settings;
// --release-cmd "" drops the command.
func cmdProfile(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	usage := "usage: hivemill project profile ID [--sensitive GLOB]... [--tier-rules /abs/tier_rules.json] [--release-cmd CMD] [--clear]"
	s := sqliteOf(r, stderr)
	if s == nil {
		return 1
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	id := args[0]
	if !projectExists(s, id) {
		return fail(stderr, fmt.Errorf("%w: no project %s", hm.ErrRefused, id))
	}
	fs := newFlags("project profile", stderr)
	var sens multi
	fs.Var(&sens, "sensitive", "a sensitive path glob (repeat); replaces sensitive_paths of the tier rules for this project")
	rules := fs.String("tier-rules", "", "the project's own tier rules file (absolute path, the ci/tier_rules.json format)")
	clear := fs.Bool("clear", false, "drop the project's sensitive paths and rules file")
	release := fs.String("release-cmd", "", "the command a release task runs (without a shell; --dry-run is added for the dry run)")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	setRelease := false
	fs.Visit(func(f *flag.Flag) { setRelease = setRelease || f.Name == "release-cmd" })
	if setRelease {
		if err := s.SetReleaseCmd(id, strings.TrimSpace(*release)); err != nil {
			return fail(stderr, err)
		}
	}
	p, err := s.Profile(id)
	if err != nil {
		return fail(stderr, err)
	}
	changed := *clear || len(sens) > 0 || *rules != ""
	if *clear {
		p.SensitivePaths, p.TierRules = nil, ""
	}
	if len(sens) > 0 {
		p.SensitivePaths = nil
		for _, g := range sens {
			if g = strings.TrimSpace(g); g == "" || strings.Contains(g, ",") {
				return fail(stderr, fmt.Errorf("%w: --sensitive %q: one glob per flag, no commas", hm.ErrUsage, g))
			}
			p.SensitivePaths = append(p.SensitivePaths, g)
		}
	}
	if *rules != "" {
		if !filepath.IsAbs(*rules) {
			return fail(stderr, fmt.Errorf("%w: --tier-rules needs an absolute path", hm.ErrUsage))
		}
		p.TierRules = *rules
	}
	if changed {
		if p.TierRules != "" { // a rules file that does not load would leave every tier undecided: refuse it now
			if _, err := risktier.LoadRules(p.TierRules); err != nil {
				return fail(stderr, fmt.Errorf("%w: --tier-rules: %v", hm.ErrRefused, err))
			}
		}
		if err := s.SetProfile(p); err != nil {
			return fail(stderr, err)
		}
	}
	_, from, err := r.TierRules(id)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%s sensitive_paths %q tier_rules %q (tier rules: %s)\n", id, strings.Join(p.SensitivePaths, ","), p.TierRules, from)
	cmd, err := s.ReleaseCmd(id)
	if err != nil {
		return fail(stderr, err)
	}
	if cmd != "" {
		fmt.Fprintf(stdout, "%s release_cmd %q\n", id, cmd)
	}
	return 0
}

func projectExists(s *hm.SQLite, id string) bool {
	ps, err := s.Projects()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

// cmdConfig is `hivemill config [KEY [VALUE]]`: the runner's keys (hm_config) and the factory keys a standalone home
// needs (noreply_email, scrub_terms_file, public_text_repos, frozen_repos, required_checks), each checked.
func cmdConfig(r *hm.Runner, args []string, stdout, stderr io.Writer) int {
	_, factoryKey := hm.FactoryKeys[arg0(args)]
	switch {
	case len(args) == 0:
		for _, k := range hm.DefaultKeys() {
			fmt.Fprintf(stdout, "%s=%s\n", k, r.Store.Cfg(k))
		}
		for _, k := range hm.FactoryKeyNames() {
			fmt.Fprintf(stdout, "%s=%s\n", k, r.Store.FactoryCfg(k))
		}
	case len(args) == 1 && factoryKey:
		fmt.Fprintln(stdout, r.Store.FactoryCfg(args[0]))
	case len(args) == 1:
		fmt.Fprintln(stdout, r.Store.Cfg(args[0]))
	case len(args) == 2 && factoryKey:
		s := sqliteOf(r, stderr)
		if s == nil {
			return 1
		}
		if err := hm.SetFactoryKey(s, args[0], args[1]); err != nil {
			return fail(stderr, err)
		}
	case len(args) == 2:
		if err := r.Store.SetCfg(args[0], args[1]); err != nil {
			return fail(stderr, err)
		}
	default:
		fmt.Fprintln(stderr, "usage: hivemill config [KEY [VALUE]]")
		return 2
	}
	return 0
}

func arg0(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}
