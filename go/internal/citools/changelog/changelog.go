// Package changelog is the Go port of ci/changelog.py: generate CHANGELOG.md from the squash-merged PRs on
// first-parent history.
//
//	ci-changelog                         print the changelog to stdout
//	ci-changelog --write                 write it to CHANGELOG.md
//	ci-changelog --check                 exit 1 if CHANGELOG.md differs from what would be generated
//	ci-changelog --since SHA             only commits after SHA (default: the previous release commit, Since)
//	ci-changelog --release-notes VERSION print the curated ci/release-notes/VERSION.md
//	ci-changelog --prerelease VERSION    print "true" for 0.x or -suffixed versions, else "false"
//	ci-changelog --check-version VERSION exit 1 unless VERSION file, release notes and CHANGELOG match VERSION
//
// Exit codes: 0 ok, 1 --check or --check-version fails, 2 usage, git error or missing release notes.
// Until the Go scrub scanner exists (G-048 P5b) the scrub filter runs ci/scrub_scan.py through python3.
package changelog

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"factory/go/internal/citools/freshexport"
)

var (
	prSuffix       = regexp.MustCompile(`\s*\(#\d+\)$`)
	datePat        = regexp.MustCompile(`\s*\(?\b\d{4}-\d{2}-\d{2}\b\)?`) // entries carry no dates
	versionHeading = regexp.MustCompile(`(?m)^## \[([^\]]+)\]`)
	upgradeLine    = regexp.MustCompile(`(?m)^### Upgrade[ \t\r\f\v]*$`)
	sections       = []string{"Added", "Changed", "Fixed", "Security"}
	// First match wins, in this order: fix/bug, then security words, then add/new, else Changed.
	rules = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"Fixed", regexp.MustCompile(`(?i)\b(fix|bug)`)},
		{"Security", regexp.MustCompile(`(?i)\b(guard|deny|harden|secret|scrub)`)},
		{"Added", regexp.MustCompile(`(?i)\b(add|new)\b`)},
	}
)

const header = `# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.4.0] - 2026-11-11
`

// Since is the previous release (tag v0.3.0); the generated list covers the commits after it. Update it with
// header at each release (the same constants are in ci/changelog.py until P5e).
const Since = "318e55a1c"

// Older releases sit below the generated lists of the newest one; each carries only its curated notes.
const older = `
## [0.3.0] - 2026-11-05

## [0.2.0] - 2026-10-30

## [0.1.0] - Initial public release

First public release of the factory, published from a fresh repository.
`

type changelogError struct{ msg string }

func (e changelogError) Error() string { return e.msg }

func failf(format string, a ...any) error { return changelogError{fmt.Sprintf(format, a...)} }

// strerror mimics Python's OSError.strerror for the errors the tool reports.
func strerror(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied"
	}
	return err.Error()
}

// tool carries the root of the factory checkout (where ci/release-notes and VERSION live) and the scanner dir.
type tool struct {
	root, ciDir string
}

func (t tool) notesPath(version string) string {
	return filepath.Join(t.root, "ci", "release-notes", version+".md")
}

func (t tool) releaseNotes(version string) (string, error) {
	data, err := os.ReadFile(t.notesPath(version))
	if err != nil {
		return "", failf("cannot read ci/release-notes/%s.md: %s. Write the curated notes for %s first.", version, strerror(err), version)
	}
	return strings.TrimSpace(string(data)) + "\n", nil
}

// IsPrerelease: a "-" suffix or a 0.x version.
func IsPrerelease(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	return strings.Contains(version, "-") || major == "0"
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// withCuratedNotes inserts ci/release-notes/<version>.md under each `## [<version>]` heading of the text.
func (t tool) withCuratedNotes(text string) (string, error) {
	var out strings.Builder
	pos := 0
	for _, m := range versionHeading.FindAllStringSubmatchIndex(text, -1) {
		version := text[m[2]:m[3]]
		if !isFile(t.notesPath(version)) {
			continue
		}
		// keep the heading and its intro paragraph, then the curated block
		stop := len(text)
		if nxt := strings.Index(text[m[1]:], "\n## "); nxt >= 0 {
			stop = m[1] + nxt
		}
		notes, err := t.releaseNotes(version)
		if err != nil {
			return "", err
		}
		out.WriteString(strings.TrimRight(text[pos:stop], "\n") + "\n\n" + notes)
		pos = stop
	}
	out.WriteString(text[pos:])
	return out.String(), nil
}

func gitLog(repo string, args ...string) (string, string, int, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return o.String(), e.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return "", "", 0, failf("cannot run git: %v", err)
	}
	return o.String(), e.String(), 0, nil
}

func knownCommit(sha, repo string) bool {
	_, _, rc, err := gitLog(repo, "cat-file", "-e", sha+"^{commit}")
	return err == nil && rc == 0
}

// releaseSubjectPrefix is the release PR's own squash subject ("Release v<VERSION> (#N)"): it never lists itself.
func (t tool) releaseSubjectPrefix() (string, error) {
	data, err := os.ReadFile(filepath.Join(t.root, "VERSION"))
	if err != nil {
		return "", failf("cannot read VERSION: %v. Restore the VERSION file.", err)
	}
	return "Release v" + strings.TrimSpace(string(data)), nil
}

func (t tool) mergedSubjects(ref, since, repo string) ([]string, error) {
	if since == "" && knownCommit(Since, repo) { // a repo without the release commit (a test repo) lists its whole history
		since = Since
	}
	rng := ref
	if since != "" {
		rng = since + ".." + ref
	}
	out, stderr, rc, err := gitLog(repo, "log", "--first-parent", "--format=%H%x09%cs%x09%s", rng)
	if err != nil {
		return nil, err
	}
	if rc != 0 {
		return nil, failf("git log %s failed: %s. Fetch the ref or pass --ref/--since a valid one.", rng, strings.TrimSpace(stderr))
	}
	release, err := t.releaseSubjectPrefix()
	if err != nil {
		return nil, err
	}
	var subjects []string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 && prSuffix.MatchString(parts[2]) && !strings.HasPrefix(parts[2], release) {
			subjects = append(subjects, strings.TrimSpace(datePat.ReplaceAllString(prSuffix.ReplaceAllString(parts[2], ""), "")))
		}
	}
	return subjects, nil
}

func sectionOf(subject string) string {
	for _, r := range rules {
		if r.re.MatchString(subject) {
			return r.name
		}
	}
	return "Changed"
}

// render returns the markdown and the number of subjects dropped by the scrub patterns.
func (t tool) render(subjects []string) (string, int, error) {
	hits, err := freshexport.ScanLines(t.ciDir, false, subjects)
	if err != nil {
		return "", 0, failf("%v", err)
	}
	groups := map[string][]string{}
	dropped := 0
	for i, subj := range subjects {
		if hits[i+1] {
			dropped++
			continue
		}
		s := sectionOf(subj)
		groups[s] = append(groups[s], "- "+subj)
	}
	head, err := t.withCuratedNotes(header)
	if err != nil {
		return "", 0, err
	}
	out := []string{head, "### All merged changes since v0.3.0\n"}
	for _, name := range sections {
		if len(groups[name]) > 0 {
			out = append(out, "#### "+name+"\n\n"+strings.Join(groups[name], "\n")+"\n")
		}
	}
	old, err := t.withCuratedNotes(older)
	if err != nil {
		return "", 0, err
	}
	out = append(out, strings.TrimLeft(old, "\n"))
	return strings.Join(out, "\n"), dropped, nil
}

// VersionProblems returns one message per failed release check of version in the repo at root.
func VersionProblems(version, root string) []string {
	var problems []string
	have := ""
	data, err := os.ReadFile(filepath.Join(root, "VERSION"))
	hasVersion := err == nil
	if hasVersion {
		have = strings.TrimSpace(string(data))
	}
	if !hasVersion || have != version {
		shown := "None"
		if hasVersion {
			shown = pyRepr(have)
		}
		problems = append(problems, fmt.Sprintf("VERSION file is %s, not %s. Set it to %s in VERSION.", shown, pyRepr(version), version))
	}
	notes := filepath.Join(root, "ci", "release-notes", version+".md")
	if text, err := os.ReadFile(notes); err != nil {
		problems = append(problems, fmt.Sprintf("ci/release-notes/%s.md is missing. Write the curated notes.", version))
	} else if !upgradeLine.Match(text) {
		problems = append(problems, fmt.Sprintf("ci/release-notes/%s.md has no \"### Upgrade\" line. Add the upgrade steps.", version))
	}
	log, _ := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if !regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\]`).Match(log) {
		problems = append(problems, fmt.Sprintf("CHANGELOG.md has no \"## [%s]\" heading. Run: python3 ci/changelog.py --write", version))
	}
	return problems
}

// pyRepr quotes like Python's repr for the plain strings of a version.
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// findRoot is the factory checkout holding ci/release-notes: the nearest parent of the working directory.
func findRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, "ci", "release-notes")); err == nil && st.IsDir() {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", errors.New("cannot find ci/release-notes above the working directory: run from a factory checkout or pass --root")
		}
	}
}

// Main runs the tool and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	rc, err := run(args, stdout, stderr)
	switch {
	case err == nil:
		return rc
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	}
	fmt.Fprintf(stderr, "changelog: error: %v\n", err)
	return 2
}

var errUsage = errors.New("usage")

func run(args []string, stdout, stderr io.Writer) (int, error) {
	fl := flag.NewFlagSet("changelog.py", flag.ContinueOnError)
	fl.SetOutput(stderr)
	ref := fl.String("ref", "origin/main", "ref to read (default origin/main)")
	since := fl.String("since", "", "only commits after SHA (default "+Since+" when the repo has it)")
	repo := fl.String("repo", ".", "git repository (default .)")
	file := fl.String("file", "CHANGELOG.md", "changelog file for --write/--check (default CHANGELOG.md)")
	notes := fl.String("release-notes", "", "print the curated notes of VERSION and exit")
	pre := fl.String("prerelease", "", `print "true" if VERSION is a pre-release (0.x or -suffix), else "false"`)
	chkVer := fl.String("check-version", "", "exit 1 unless VERSION file, release notes and CHANGELOG all match VERSION")
	write := fl.Bool("write", false, "write the changelog to --file")
	check := fl.Bool("check", false, "exit 1 if --file differs from the generated text")
	rootFlag := fl.String("root", "", "factory checkout holding ci/ and VERSION (default: found from the working directory)")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, err
		}
		return 2, errUsage // the flag package already printed the problem
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if fl.NArg() > 0 || (*write && *check) {
		fmt.Fprintln(stderr, "changelog.py: error: unexpected argument, or --write and --check together")
		return 2, errUsage
	}
	root := *rootFlag
	if root == "" {
		r, err := findRoot()
		if err != nil {
			return 0, failf("%v", err)
		}
		root = r
	}
	t := tool{root: root, ciDir: filepath.Join(root, "ci")}
	if set["release-notes"] {
		text, err := t.releaseNotes(*notes)
		if err != nil {
			return 0, err
		}
		io.WriteString(stdout, text)
		return 0, nil
	}
	if set["prerelease"] {
		if IsPrerelease(*pre) {
			fmt.Fprintln(stdout, "true")
		} else {
			fmt.Fprintln(stdout, "false")
		}
		return 0, nil
	}
	if set["check-version"] {
		problems := VersionProblems(*chkVer, root)
		for _, m := range problems {
			fmt.Fprintf(stderr, "changelog: check-version: %s\n", m)
		}
		if len(problems) > 0 {
			return 1, nil
		}
		return 0, nil
	}
	subjects, err := t.mergedSubjects(*ref, *since, *repo)
	if err != nil {
		return 0, err
	}
	text, dropped, err := t.render(subjects)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(stderr, "changelog: %d subject(s) dropped by scrub patterns\n", dropped)
	switch {
	case *check:
		current, err := os.ReadFile(*file)
		if err != nil {
			return 0, failf("cannot read %s: %s. Run changelog.py --write first.", *file, strerror(err))
		}
		if string(current) != text {
			fmt.Fprintf(stderr, "changelog: %s is out of date. Run: python3 ci/changelog.py --write\n", *file)
			return 1, nil
		}
	case *write:
		if err := os.WriteFile(*file, []byte(text), 0o644); err != nil {
			return 0, failf("cannot write %s: %s", *file, strerror(err))
		}
	default:
		io.WriteString(stdout, text)
	}
	return 0, nil
}

// ReleaseNotes is the curated notes text of version under the checkout at root, as `--release-notes` prints it.
func ReleaseNotes(root, version string) (string, error) {
	return tool{root: root}.releaseNotes(version)
}
