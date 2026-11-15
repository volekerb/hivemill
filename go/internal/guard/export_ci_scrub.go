package guard

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Go port of ci/scrub_scan.py (T-1485): CIScrubScanMain is the whole CLI. Same flags, same output lines, exit codes
// 0 no hits, 1 hits, 2 usage or runtime error. Owner terms come only from $SCRUB_TERMS_FILE and --terms-file, and
// only their count is printed.

var ciScrubSpec = ciArgSpec{
	"--tree": ciArgOpt, "--history": ciArgOpt, "--path": ciArgMany, "--exclude-ref": ciArgMany,
	"--history-allowlist": ciArgOne, "--show-line-hash": ciArgFlag, "--terms-file": ciArgOne, "--allowlist": ciArgOne,
}

const ciScrubPatternIDs = "aws-access-key-id, anthropic-key, cgnat, email, env-file, generic-secret, github-pat, github-token, " +
	"high-entropy-id, home-path, oauth-secret, owner-term, private-key, railway-token, rfc1918"

// ciLogFlags is scrub_scan.LOG_FLAGS.
var ciLogFlags = []string{"--all", "--root", "-m", "-p", "--text", "--no-textconv", "--no-ext-diff", "--no-renames",
	"--no-color", "--no-use-mailmap", "--src-prefix=a/", "--dst-prefix=b/"}

const ciHistoryAllowlistPath = "ci/scrub_history_allowlist.txt"

// ciScrubReport is scrub_scan.Report.
type ciScrubReport struct {
	sc        *ciScanner
	out       *ciOut
	counts    map[string]int
	allowlist int
	skipped   []string
}

func (r *ciScrubReport) hit(pid, loc, masked, extra string) {
	r.counts[pid]++
	r.out.printf("%s %s %s%s\n", pid, r.sc.hideTerms(loc), masked, extra)
}

func (r *ciScrubReport) pathHit(pid, rel, line string, allow *ciAllowlist) {
	if allow != nil && allow.allows([]string{rel}, line, pid) {
		r.allowlist++
		return
	}
	r.hit(pid, rel+":0", ciMask(r.sc.hideTerms(rel[strings.LastIndexByte(rel, '/')+1:])), "")
}

func (r *ciScrubReport) scanText(text, path string, allow *ciAllowlist, names []string) {
	for n, line := range pyLines(text) {
		for _, p := range r.sc.scanLine(line) {
			if allow != nil && allow.allows(names, line, p.id) {
				r.allowlist++
			} else {
				r.hit(p.id, path+":"+strconv.Itoa(n+1), p.masked, "")
			}
		}
	}
}

func (r *ciScrubReport) scanFile(fsPath, shown string, allow *ciAllowlist, names []string) error {
	data, err := os.ReadFile(fsPath)
	if err != nil {
		return &usageError{"cannot read " + pyRepr(shown) + ": " + pyStrerror(err)}
	}
	if bytes.IndexByte(data[:min(len(data), ciBinaryProbe)], 0) >= 0 {
		r.skipped = append(r.skipped, shown)
		return nil
	}
	r.scanText(pyDecodeReplace(data), shown, allow, names)
	return nil
}

func (r *ciScrubReport) finish(mode string, withAllowlist bool, stderr *ciOut) int {
	ids := make([]string, 0, len(r.counts))
	total := 0
	for id, n := range r.counts {
		ids = append(ids, id)
		total += n
	}
	sort.Strings(ids)
	for _, id := range ids {
		r.out.printf("  %s: %d\n", id, r.counts[id])
	}
	if mode != "history" || withAllowlist {
		r.out.printf("%d hits not allowlisted, %d allowlisted\n", total, r.allowlist)
	} else {
		r.out.printf("%d hits\n", total)
	}
	if len(r.skipped) > 0 {
		stderr.printf("scrub-scan: skipped %d binary file(s): not text-scannable\n", len(r.skipped))
	}
	if total > 0 {
		return 1
	}
	return 0
}

// ciIsEnvFile is scrub_scan.is_env_file.
func ciIsEnvFile(path string) bool {
	name := pyLowerFull(path[strings.LastIndexByte(path, '/')+1:])
	if name == ".env.example" || name == ".env.sample" {
		return false
	}
	return strings.HasSuffix(name, ".env") || strings.HasPrefix(name, ".env.")
}

// ciGitScrub runs git as scrub_scan.git does: no timeout, the caller's environment.
func ciGitScrub(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, &usageError{"git " + args[0] + " failed in " + pyRepr(dir) + ": " + strings.TrimFunc(pyDecodeReplace(stderr.Bytes()), pyIsSpace) +
			". Pass a directory inside a git repository."}
	}
	return nil, &usageError{"git not found on PATH"}
}

func ciScanTree(dir string, r *ciScrubReport, allow *ciAllowlist) error {
	out, err := ciGitScrub(dir, "ls-files", "-z")
	if err != nil {
		return err
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		full := rel
		if dir != "" {
			full = strings.TrimRight(dir, "/") + "/" + rel
		}
		if lst, err := os.Lstat(full); err != nil || lst.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
			continue
		}
		if ciIsEnvFile(rel) {
			r.pathHit(ciEnvFile, rel, rel, allow)
		}
		if err := r.scanFile(full, rel, allow, []string{rel}); err != nil {
			return err
		}
	}
	return nil
}

// ciGitEnvScrub is scrub_scan.git_env: the environment minus GIT_*, plus the hardening.
func ciGitEnvScrub() []string { return ptGitEnv() }

func ciRandomMark() (string, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", &usageError{"cannot read random bytes for the git log marker: " + err.Error()}
	}
	return "\x01" + hex.EncodeToString(rnd[:]) + "\x01", nil
}

// ciHistoryHit is one hit of the history scan; entry is non-nil for a blob-content hit.
type ciHistoryHit struct {
	pid, loc, masked string
	entryPath        string
	entrySha         string
	hasEntry         bool
}

func tail2000(s string) string {
	rs := []rune(strings.TrimFunc(s, pyIsSpace))
	if len(rs) > 2000 {
		rs = rs[len(rs)-2000:]
	}
	return string(rs)
}

// ciHistory is history_hits: emit is called for every hit, in the order of the Python generator.
func ciHistory(dir string, sc *ciScanner, excludes []string, emit func(ciHistoryHit)) error {
	mark, err := ciRandomMark()
	if err != nil {
		return err
	}
	format := "--format=" + mark + "H%H%n" + mark + "A%an <%ae>%n" + mark + "C%cn <%ce>%n" + mark + "M%n%B" + mark + "E"
	args := []string{"-C", dir, "-c", "core.quotepath=off", "-c", "i18n.logOutputEncoding=UTF-8", "-c", "log.showSignature=false", "log"}
	for _, r := range excludes {
		args = append(args, "--exclude="+r)
	}
	args = append(args, ciLogFlags...)
	args = append(args, format)
	cmd := exec.Command("git", args...)
	cmd.Env = ciGitEnvScrub()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr // read after Wait: the pipe copy is done by os/exec, so git cannot block on a full stderr pipe
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git log stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return &usageError{"git not found on PATH"}
	}
	lineErr := ciHistoryLines(bufio.NewReaderSize(stdout, 1<<20), mark, sc, emit)
	if lineErr != nil {
		// stop git before waiting: it may be blocked writing to a pipe nobody reads any more
		if kerr := cmd.Process.Kill(); kerr != nil {
			lineErr = fmt.Errorf("%w (kill git: %w)", lineErr, kerr)
		}
		_ = cmd.Wait() // the exit status of a killed git carries no information; lineErr is the failure
		return lineErr
	}
	if werr := cmd.Wait(); werr != nil {
		return &usageError{"git log failed in " + pyRepr(dir) + ": " + tail2000(pyDecodeReplace(stderr.Bytes())) + ". Pass a directory inside a git repository."}
	}
	if err := ciHeaderHits(dir, sc, excludes, emit); err != nil {
		return err
	}
	return ciRefHits(dir, sc, excludes, emit)
}

var ciHistAllowlistPathField = regexp.MustCompile(`^[` + rxWord + `./-]+$`)

// ciIsAllowlistEntry is _is_allowlist_entry: a well-formed line of the committed history allowlist.
func ciIsAllowlistEntry(sc *ciScanner, text string) bool {
	m := ciHistoryEntry.FindStringSubmatch(text)
	if m == nil || !ciHistAllowlistPathField.MatchString(m[1]) {
		return false
	}
	for _, field := range []string{m[1], m[4]} {
		for _, p := range sc.scanLine(field) {
			if p.id == "generic-secret" {
				return false
			}
		}
	}
	return true
}

func ciHistoryLines(rd *bufio.Reader, mark string, sc *ciScanner, emit func(ciHistoryHit)) error {
	commit, state := "", "msg"
	var path []byte
	inHeader := false
	for {
		raw, rerr := rd.ReadBytes('\n')
		if len(raw) == 0 && rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read git log: %w", rerr)
		}
		body := bytes.TrimSuffix(raw, []byte("\n"))
		line := pyDecodeReplace(body)
		who := ""
		switch {
		case strings.HasPrefix(line, mark+"H"):
			commit, state, path, inHeader = line[len(mark)+1:], "head", nil, false
			continue
		case state == "head" && strings.HasPrefix(line, mark+"A"):
			who = "author"
		case state == "head" && strings.HasPrefix(line, mark+"C"):
			who = "committer"
		}
		if who != "" {
			for _, p := range sc.scanLine(line[len(mark)+1:]) {
				emit(ciHistoryHit{pid: p.id, loc: commit + ":" + who, masked: p.masked})
			}
			continue
		}
		if state == "head" && strings.HasPrefix(line, mark+"M") {
			state = "msg"
			continue
		}
		if state == "msg" {
			end := strings.Index(line, mark+"E")
			text := line
			if end >= 0 {
				text = line[:end]
			}
			for _, p := range sc.scanLine(text) {
				emit(ciHistoryHit{pid: p.id, loc: commit + ":message", masked: p.masked})
			}
			if end >= 0 {
				state = "diff"
			}
			continue
		}
		if bytes.HasPrefix(body, []byte("diff --git ")) {
			p, err := diffHeaderPath(body)
			if err != nil {
				return err
			}
			path, inHeader = p, true
			text := pyDecodeReplace(path)
			shown := sc.shown(path)
			for _, h := range sc.scanLine(text) {
				emit(ciHistoryHit{pid: h.id, loc: commit + ":" + shown + " (path name)", masked: h.masked})
			}
			if ciIsEnvFile(text) {
				emit(ciHistoryHit{pid: ciEnvFile, loc: commit + ":" + shown + " (path name)",
					masked: ciMask(sc.hideTerms(text[strings.LastIndexByte(text, '/')+1:]))})
			}
			continue
		}
		if inHeader {
			inHeader = !bytes.HasPrefix(body, []byte("@@"))
			continue
		}
		if len(body) == 0 || (body[0] != '+' && body[0] != '-') {
			continue
		}
		content := body[1:] // the diff marker would otherwise join the local part of an email
		sum := sha256.Sum256(content)
		sha := hex.EncodeToString(sum[:])
		text := pyDecodeReplace(content)
		for _, p := range sc.scanLine(text) {
			if p.id == "generic-secret" && string(path) == ciHistoryAllowlistPath && ciIsAllowlistEntry(sc, text) {
				continue
			}
			emit(ciHistoryHit{pid: p.id, loc: commit + ":" + sc.shown(path), masked: p.masked, entryPath: string(path), entrySha: sha, hasEntry: true})
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read git log: %w", rerr)
		}
	}
}

// ciHeaderHits is _header_hits: extra commit headers (mergetag, gpgsig, encoding, ...) of every commit object.
func ciHeaderHits(dir string, sc *ciScanner, excludes []string, emit func(ciHistoryHit)) error {
	args := []string{"rev-list"}
	for _, r := range excludes {
		args = append(args, "--exclude="+r)
	}
	args = append(args, "--all")
	run := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = ciGitEnvScrub()
		cmd.Stdin = bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			return out, nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, &usageError{"git " + args[0] + " failed in " + pyRepr(dir) + ": " + tail2000(pyDecodeReplace(stderr.Bytes()))}
		}
		return nil, &usageError{"git not found on PATH"}
	}
	idsOut, err := run(nil, args...)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(idsOut))
	if len(ids) == 0 {
		return nil
	}
	raw, err := run([]byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return err
	}
	pos := 0
	for _, commit := range ids {
		nl := bytes.IndexByte(raw[pos:], '\n')
		if nl < 0 {
			return &usageError{"git cat-file --batch output ended early in " + pyRepr(dir) + ". Retry."}
		}
		f := strings.Fields(string(raw[pos : pos+nl]))
		size := 0
		if len(f) >= 3 {
			size, err = strconv.Atoi(f[2])
		}
		if len(f) < 3 || err != nil || pos+nl+1+size > len(raw) {
			return &usageError{"git cat-file --batch returned an unexpected record in " + pyRepr(dir) + ". Retry."}
		}
		obj := raw[pos+nl+1 : pos+nl+1+size]
		pos += nl + 1 + size + 1
		head, _, _ := bytes.Cut(obj, []byte("\n\n"))
		name := ""
		for _, line := range strings.Split(pyDecodeReplace(head), "\n") {
			if !strings.HasPrefix(line, " ") {
				name, _, _ = strings.Cut(line, " ")
			}
			if name == "tree" || name == "parent" || name == "author" || name == "committer" { // the log scan covers these
				continue
			}
			for _, p := range sc.scanLine(line) {
				emit(ciHistoryHit{pid: p.id, loc: commit + ":header " + sc.shown([]byte(name)), masked: p.masked}) // the name is attacker-controlled text
			}
		}
	}
	return nil
}

// ciRefHits is _ref_hits: ref names and annotated tags.
func ciRefHits(dir string, sc *ciScanner, excludes []string, emit func(ciHistoryHit)) error {
	sep, err := ciRandomMark()
	if err != nil {
		return err
	}
	format := sep + "R%(refname)" + sep + "K%(objecttype)" + sep + "N%(tag)" + sep + "G%(taggername) %(taggeremail)" + sep + "C%(contents)"
	cmd := exec.Command("git", "-C", dir, "for-each-ref", "--format="+format)
	cmd.Env = ciGitEnvScrub()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &usageError{"git for-each-ref failed in " + pyRepr(dir) + ": " + strings.TrimFunc(pyDecodeReplace(stderr.Bytes()), pyIsSpace)}
		}
		return &usageError{"git not found on PATH"}
	}
	recs := strings.Split(pyDecodeReplace(out), sep+"R")
	for _, rec := range recs[1:] {
		parts := strings.Split(rec, sep)
		name, fields := parts[0], map[string]string{}
		for _, part := range parts[1:] {
			if part != "" {
				fields[part[:1]] = part[1:]
			}
		}
		excluded := false
		for _, pat := range excludes {
			if fnmatchCase(name, pat) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		for _, p := range sc.scanLine(name) {
			emit(ciHistoryHit{pid: p.id, loc: "ref:" + sc.hideAll(name), masked: p.masked})
		}
		if fields["K"] != "tag" { // a branch's %(contents) is its tip commit, which the log scan covers
			continue
		}
		texts := append([]string{fields["N"], fields["G"]}, strings.Split(fields["C"], "\n")...)
		for _, text := range texts {
			for _, p := range sc.scanLine(text) {
				emit(ciHistoryHit{pid: p.id, loc: "tag:" + sc.hideAll(name), masked: p.masked})
			}
		}
	}
	return nil
}

// CIScrubScanMain is ci/scrub_scan.py main(): args without the program name.
func CIScrubScanMain(args []string, stdout, stderr io.Writer) int {
	out, errw := &ciOut{w: stdout}, &ciOut{w: stderr}
	code := ciScrubRun(args, out, errw)
	if out.err != nil || errw.err != nil {
		if _, err := fmt.Fprintln(os.Stderr, "scrub-scan: error: cannot write output:", errors.Join(out.err, errw.err)); err != nil {
			return 2
		}
		return 2
	}
	return code
}

func ciScrubRun(args []string, out, errw *ciOut) int {
	code, err := ciScrub(args, out, errw)
	if err == nil {
		return code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		errw.printf("scrub-scan: error: %s\n", ue.Msg)
		return 2
	}
	errw.printf("scrub-scan: error: %s\n", err.Error())
	return 1
}

func ciScrubArgError(errw *ciOut, msg string) (int, error) {
	errw.printf("usage: scrub_scan.py [-h] (--tree [DIR] | --history [DIR] | --path FILE [FILE ...]) [options]\nscrub_scan.py: error: %s\n", msg)
	return 2, nil
}

func ciScrub(args []string, out, errw *ciOut) (int, error) {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			out.printf("usage: scrub_scan.py [-h] (--tree [DIR] | --history [DIR] | --path FILE [FILE ...]) [options]\n\npattern ids: %s\n", ciScrubPatternIDs)
			return 0, nil
		}
	}
	spec := ciArgSpec{"-h": ciArgFlag}
	for k, v := range ciScrubSpec {
		spec[k] = v
	}
	p, _, err := ciParseArgs(args, spec, false)
	if err != nil {
		return ciScrubArgError(errw, err.Error())
	}
	if len(p.pos) > 0 {
		return ciScrubArgError(errw, "unrecognized arguments: "+strings.Join(p.pos, " "))
	}
	modes := 0
	for _, m := range []string{"--tree", "--history", "--path"} {
		if p.has(m) {
			modes++
		}
	}
	if modes == 0 {
		return ciScrubArgError(errw, "one of the arguments --tree --history --path is required")
	}
	if modes > 1 {
		return ciScrubArgError(errw, "argument --history/--path/--tree: not allowed with another mode")
	}
	excludes := p.all("--exclude-ref")
	if len(excludes) > 0 && !p.has("--history") {
		return 0, &usageError{"--exclude-ref only applies to --history"}
	}
	termFiles := p.all("--terms-file")
	if v := os.Getenv(termsVar); v != "" {
		termFiles = append(termFiles, v)
	}
	terms, err := ciLoadTerms(termFiles)
	if err != nil {
		return 0, err
	}
	if len(terms) > 0 {
		errw.printf("scrub-scan: %d owner term(s) loaded\n", len(terms))
	} else {
		errw.printf("scrub-scan: warning: no owner terms loaded (set SCRUB_TERMS_FILE or pass --terms-file); generic patterns only\n")
	}
	sc := newCIScanner(terms, false)
	rep := &ciScrubReport{sc: sc, out: out, counts: map[string]int{}}
	_, hasHA := p.last("--history-allowlist")
	if !p.has("--history") && (hasHA || p.has("--show-line-hash")) {
		return 0, &usageError{"--history-allowlist and --show-line-hash only apply to --history"}
	}
	if p.has("--history") {
		dir := "."
		if v := p.occ["--history"][len(p.occ["--history"])-1]; len(v) > 0 {
			dir = v[0]
		}
		var hallow *ciHistoryAllowlist
		if hasHA {
			path, _ := p.last("--history-allowlist")
			if hallow, err = ciLoadHistoryAllowlist(path); err != nil {
				return 0, err
			}
		}
		show := p.has("--show-line-hash")
		herr := ciHistory(dir, sc, excludes, func(h ciHistoryHit) {
			if h.hasEntry && hallow != nil && hallow.allows(h.entryPath, h.pid, h.entrySha) {
				rep.allowlist++
				return
			}
			extra := ""
			if h.hasEntry && show {
				extra = " sha256=" + h.entrySha
			}
			rep.hit(h.pid, h.loc, h.masked, extra)
		})
		if herr != nil {
			return 0, herr
		}
		if hallow != nil {
			for _, k := range hallow.unused() {
				errw.printf("scrub-scan: warning: unused history allowlist entry %s:%s:%s...\n", sc.shown([]byte(k.path)), k.pid, k.sha[:12])
			}
		}
		return rep.finish("history", hallow != nil, errw), nil
	}
	allowPath, ok := p.last("--allowlist")
	if !ok {
		allowPath = ciDefaultAllowlist()
	}
	allow, err := ciLoadAllowlist(allowPath, errw.w)
	if err != nil {
		return 0, err
	}
	if p.has("--tree") {
		dir := "."
		if v := p.occ["--tree"][len(p.occ["--tree"])-1]; len(v) > 0 {
			dir = v[0]
		}
		if err := ciScanTree(dir, rep, allow); err != nil {
			return 0, err
		}
		for _, e := range allow.unused() {
			pid := e.pid
			if pid == "" {
				pid = "-"
			}
			errw.printf("scrub-scan: warning: unused allowlist entry %s:%s:%s\n", sc.shown([]byte(e.path)), pid, sc.hideTerms(e.src))
		}
		return rep.finish("tree", false, errw), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("working directory: %w", err)
	}
	for _, f := range p.all("--path") {
		abs, aerr := filepath.Abs(f)
		if aerr != nil {
			return 0, fmt.Errorf("absolute path of %q: %w", f, aerr)
		}
		rel, rerr := filepath.Rel(cwd, abs)
		if rerr != nil {
			rel = abs
		}
		if err := rep.scanFile(f, f, allow, []string{filepath.Clean(f), rel}); err != nil {
			return 0, err
		}
	}
	return rep.finish("path", false, errw), nil
}
