// Command diff-checklist matches reliability-checklist items to a diff.
//
//	diff-checklist run      [--repo-dir D] [--base origin/main] [--items-ref REF]
//	diff-checklist body     --pr <url|N> [--repo owner/name] [--repo-dir D] [--items-ref REF]
//	diff-checklist validate --file F
//
// Exit 0: ok. Exit 1: problems in enforce mode, or validate errors.
// Exit 2: usage error, a git/gh failure, an unreadable or invalid items file
// on the ref, or a diff over 20 MiB (fails closed). Items are read from the
// ref, never from the worktree. See docs/diff-checklist.md.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"factory/go/internal/checklist"
)

const itemsPath = "ci/diff-checklist.json"

// maxDiff caps the diff bytes read; a var so tests can lower it.
var maxDiff int64 = 20 << 20

var errTooBig = errors.New("output over the size cap")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "diff-checklist: internal error: %v\n", r)
			code = 2
		}
	}()
	if len(args) == 0 {
		return usage(stderr, "a subcommand is required")
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "body":
		return cmdBody(args[1:], stdout, stderr)
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	}
	return usage(stderr, fmt.Sprintf("unknown subcommand %q", args[0]))
}

func usage(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "diff-checklist: %s\nusage: diff-checklist run [--repo-dir D] [--base origin/main] [--items-ref REF]\n"+
		"       diff-checklist body --pr <url|N> [--repo owner/name] [--repo-dir D] [--items-ref REF]\n"+
		"       diff-checklist validate --file F\n", msg)
	return 2
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func fail(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "diff-checklist: "+format+"\n", a...)
	return 2
}

// capped runs argv with no shell and returns stdout, failing when it exceeds max bytes.
func capped(max int64, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	data, rerr := io.ReadAll(io.LimitReader(out, max+1))
	tooBig := int64(len(data)) > max
	if tooBig || rerr != nil {
		if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			return nil, fmt.Errorf("kill %s: %w", name, kerr)
		}
		// Close our read end so grandchildren writing to it get SIGPIPE and
		// release the stderr pipe Wait is about to drain.
		if cerr := out.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			return nil, fmt.Errorf("close %s output: %w", name, cerr)
		}
	}
	werr := cmd.Wait()
	switch {
	case tooBig:
		return nil, fmt.Errorf("%s: %w (%d bytes max)", name, errTooBig, max)
	case rerr != nil:
		return nil, fmt.Errorf("read %s output: %w", name, rerr)
	case werr != nil:
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), werr, strings.TrimSpace(errBuf.String()))
	}
	return data, nil
}

func git(dir string, args ...string) ([]byte, error) {
	return capped(maxDiff, "git", append([]string{"-C", dir}, args...)...)
}

func checkRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid ref %q", ref)
	}
	return nil
}

// loadItems reads ci/diff-checklist.json from ref via git show. present is
// false when the ref has no such file. The worktree copy is never read.
func loadItems(dir, ref string) (list *checklist.List, sha string, present bool, err error) {
	if err = checkRef(ref); err != nil {
		return nil, "", false, err
	}
	shaOut, err := git(dir, "rev-parse", "--verify", "--quiet", "--short", ref+"^{commit}")
	if err != nil {
		return nil, "", false, fmt.Errorf("ref %s not found in %s: %w", ref, dir, err)
	}
	sha = strings.TrimSpace(string(shaOut))
	ls, err := git(dir, "ls-tree", "--full-tree", "--name-only", ref, "--", itemsPath)
	if err != nil {
		return nil, "", false, fmt.Errorf("list %s on %s: %w", itemsPath, ref, err)
	}
	if strings.TrimSpace(string(ls)) == "" {
		return nil, sha, false, nil
	}
	data, err := git(dir, "show", ref+":"+itemsPath)
	if err != nil {
		return nil, "", false, fmt.Errorf("read %s on %s: %w", itemsPath, ref, err)
	}
	list, err = checklist.Load(data)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid %s on %s:\n%w", itemsPath, ref, err)
	}
	return list, sha, true, nil
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", stderr)
	dir := fs.String("repo-dir", ".", "git repository directory")
	base := fs.String("base", "origin/main", "base ref; the diff is merge-base(base, HEAD)..HEAD")
	itemsRef := fs.String("items-ref", "", "ref to read items from (default: --base)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return usage(stderr, "unexpected argument "+fs.Arg(0))
	}
	ref := *itemsRef
	if ref == "" {
		ref = *base
	}
	list, sha, present, err := loadItems(*dir, ref)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if !present {
		fmt.Fprintf(stdout, "diff-checklist: no %s on %s: nothing to run\n", itemsPath, ref)
		return 0
	}
	if err := checkRef(*base); err != nil {
		return fail(stderr, "%v", err)
	}
	mb, err := git(*dir, "merge-base", *base, "HEAD")
	if err != nil {
		return fail(stderr, "merge-base %s HEAD: %v", *base, err)
	}
	diff, err := git(*dir, "-c", "core.quotepath=false", "diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", "-M", strings.TrimSpace(string(mb)), "HEAD")
	if err != nil {
		return diffFail(stderr, err)
	}
	matched := checklist.Match(list.Items, checklist.ParseDiff(string(diff)))
	fmt.Fprint(stdout, checklist.RenderBlock(ref, sha, list.Mode, matched, len(list.Items)))
	return 0
}

func diffFail(stderr io.Writer, err error) int {
	if errors.Is(err, errTooBig) {
		return fail(stderr, "diff is over the %d MiB limit: %v; split the change or run the checklist by hand", maxDiff>>20, err)
	}
	return fail(stderr, "diff: %v", err)
}

func cmdBody(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("body", stderr)
	pr := fs.String("pr", "", "PR URL or number (required)")
	repo := fs.String("repo", "", "owner/name (for a PR number)")
	dir := fs.String("repo-dir", ".", "git repository directory holding the items ref")
	itemsRef := fs.String("items-ref", "origin/main", "ref to read items from")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pr == "" || fs.NArg() > 0 {
		return usage(stderr, "body needs --pr <url|N>")
	}
	if strings.HasPrefix(*pr, "-") || strings.HasPrefix(*repo, "-") {
		return usage(stderr, "--pr and --repo must not start with '-'")
	}
	list, _, present, err := loadItems(*dir, *itemsRef)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if !present {
		fmt.Fprintf(stdout, "diff-checklist: no %s on %s: nothing to run\n", itemsPath, *itemsRef)
		return 0
	}
	ghArgs := func(sub string, extra ...string) []string {
		a := []string{"pr", sub, *pr}
		if *repo != "" {
			a = append(a, "-R", *repo)
		}
		return append(a, extra...)
	}
	diff, err := capped(maxDiff, "gh", ghArgs("diff")...)
	if err != nil {
		return diffFail(stderr, err)
	}
	view, err := capped(maxDiff, "gh", ghArgs("view", "--json", "body")...)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	var v struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(view, &v); err != nil {
		return fail(stderr, "gh pr view output is not JSON with a body: %v", err)
	}
	matched := checklist.Match(list.Items, checklist.ParseDiff(string(diff)))
	problems := 0
	var ids []string
	for _, r := range checklist.CheckBody(matched, v.Body) {
		fmt.Fprintln(stdout, r.Line())
		if r.Status != "OK" {
			problems++
			ids = append(ids, r.ID)
		}
	}
	fmt.Fprintf(stdout, "diff-checklist: matched %d of %d; problems %d; mode %s\n", len(matched), len(list.Items), problems, list.Mode)
	if problems == 0 {
		return 0
	}
	if list.Mode == checklist.ModeObserve {
		fmt.Fprintf(stdout, "observe: would block: %s\n", strings.Join(ids, ","))
		return 0
	}
	return 1
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("validate", stderr)
	file := fs.String("file", "", "items file to check (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" || fs.NArg() > 0 {
		return usage(stderr, "validate needs --file F")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return fail(stderr, "read %s: %v", *file, err)
	}
	list, err := checklist.Load(data)
	if err != nil {
		fmt.Fprintf(stderr, "diff-checklist: %s is invalid:\n%v\n", *file, err)
		return 1
	}
	fmt.Fprintf(stdout, "diff-checklist: %s ok (%d items, mode %s)\n", *file, len(list.Items), list.Mode)
	return 0
}
