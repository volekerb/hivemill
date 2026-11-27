package guard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Go port of ci/public_text_guard.py (T-1485): CIPublicTextGuardMain is the whole CLI; ciCheckPush is check_push with
// the locations and masked text the CLI prints (ptcheck.go's checkPush returns only ids for the worker guard and is
// left alone). Exit codes: 0 clean, 1 hits, 2 usage or runtime error.

// ciHit is one (pattern id, location, masked text) hit.
type ciHit struct{ id, loc, masked string }

// ciCheckPush is public_text_guard.check_push(repo, base, head, terms, allowlist).
func ciCheckPush(repo, base, head string, sc *ciScanner, allow *ciAllowlist) ([]ciHit, error) {
	for _, rev := range []string{base, head} {
		if strings.HasPrefix(rev, "-") {
			return nil, &usageError{"revision " + pyRepr(rev) + " looks like an option. Pass a commit id or branch name."}
		}
	}
	mark, err := ciRandomMark()
	if err != nil {
		return nil, err
	}
	format := "--format=" + mark + "H%H%n" + mark + "A%an <%ae>%n" + mark + "C%cn <%ce>%n" + mark + "M%n%B" + mark + "E"
	args := append([]string{"log", "--cc"}, logFlags...)
	args = append(args, base+".."+head, format, "--")
	out, err := ptGit(repo, args...)
	if err != nil {
		return nil, err
	}
	var hits []ciHit
	add := func(loc string, ps []ciPair) {
		for _, p := range ps {
			hits = append(hits, ciHit{p.id, loc, p.masked})
		}
	}
	commit, full, state := "", "", "msg"
	var path []byte
	newline, width := 0, 1
	inHeader := false
	skip := -1 // -1: not decided for this file yet; 1: verified image, its added lines are not text
	for _, raw := range bytes.Split(out, []byte("\n")) {
		line := pyDecodeReplace(raw)
		switch {
		case strings.HasPrefix(line, mark+"H"):
			full, state, path, inHeader, skip = line[len(mark)+1:], "head", nil, false, -1
			commit = string([]rune(full)[:min(8, len([]rune(full)))])
			continue
		case state == "head" && (strings.HasPrefix(line, mark+"A") || strings.HasPrefix(line, mark+"C")):
			who := "committer"
			if strings.HasPrefix(line, mark+"A") {
				who = "author"
			}
			add("commit:"+commit+":"+who, sc.scanLine(line[len(mark)+1:]))
			continue
		case state == "head" && strings.HasPrefix(line, mark+"M"):
			state = "msg"
			continue
		case state == "msg":
			end := strings.Index(line, mark+"E")
			text := line
			if end >= 0 {
				text = line[:end]
			}
			add("commit:"+commit+":message", sc.scanLine(text))
			if end >= 0 {
				state = "diff"
			}
			continue
		}
		if bytes.HasPrefix(raw, []byte("diff --git ")) || bytes.HasPrefix(raw, []byte("diff --cc ")) {
			if bytes.HasPrefix(raw, []byte("diff --cc ")) { // combined (merge) diff: one path, no a/ b/ prefix
				rest := raw[len("diff --cc "):]
				if len(rest) > 0 && rest[0] == '"' {
					p, err := cUnquote(rest)
					if err != nil {
						return nil, err
					}
					path = p
				} else {
					path = rest
				}
			} else {
				p, err := diffHeaderPath(raw)
				if err != nil {
					return nil, err
				}
				path = p
			}
			inHeader, skip = true, -1
			add("commit:"+commit+":path", sc.scanLine(pyDecodeReplace(path)))
			continue
		}
		var hunkNo []byte
		isHunk := false
		if bytes.HasPrefix(raw, []byte("@@@")) {
			if m := ccHunkRe.FindSubmatch(raw); m != nil {
				isHunk, hunkNo = true, m[2]
				width = len(m[1]) - 1
			}
		}
		if !isHunk && bytes.HasPrefix(raw, []byte("@@")) {
			if m := hunkRe.FindSubmatch(raw); m != nil {
				isHunk, hunkNo = true, m[1]
				width = 1
			}
		}
		if inHeader && !isHunk {
			continue
		}
		if isHunk {
			inHeader = false
			n, aerr := strconv.Atoi(string(hunkNo))
			if aerr != nil {
				return nil, &usageError{"cannot parse a hunk header line number in the git log output. Report this to the scanner owner."}
			}
			newline = n
			continue
		}
		w := min(width, len(raw))
		if bytes.Equal(raw[:w], bytes.Repeat([]byte{'+'}, width)) {
			if skip < 0 {
				skip = 0
				if blob, ok := ptBlob(repo, full, path); ok {
					if payloads, ok := imagePayloads(blob); ok {
						skip = 1
						where := sc.shown(path)
						for _, p := range payloads {
							for _, ln := range pyLines(pyDecodeReplace(p.data)) {
								add(where+":"+p.part, sc.scanLine(ln))
							}
						}
					}
				}
			}
			if skip == 1 {
				continue
			}
			content := pyDecodeReplace(raw[width:])
			for _, p := range sc.scanLine(content) {
				if !allow.allows([]string{string(path)}, content, p.id) {
					hits = append(hits, ciHit{p.id, sc.shown(path) + ":" + strconv.Itoa(newline), p.masked})
				}
			}
			newline++
		} else if !bytes.Contains(raw[:w], []byte("-")) {
			newline++
		}
	}
	return hits, nil
}

var ciPTGSpec = ciArgSpec{"--terms-file": ciArgOne, "--no-terms": ciArgFlag, "-h": ciArgFlag}

// ciPTGArgs is the parsed command line of public_text_guard.py.
type ciPTGArgs struct {
	mode      string
	termFiles []string
	noTerms   bool
	file      string
	names     []string
	repo      string
	allowlist string
	hasAllow  bool
	base      string
	head      string
}

func ciPTGParse(args []string) (*ciPTGArgs, error) {
	main, rest, err := ciParseArgs(args, ciPTGSpec, true)
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 {
		return nil, errors.New("the following arguments are required: mode")
	}
	a := &ciPTGArgs{mode: rest[0], repo: ".", file: "-"}
	subSpec := ciArgSpec{"--terms-file": ciArgOne, "--no-terms": ciArgFlag}
	switch a.mode {
	case "text", "ref":
	case "push":
		subSpec["--repo"], subSpec["--allowlist"] = ciArgOne, ciArgOne
	default:
		return nil, fmt.Errorf("argument mode: invalid choice: %s (choose from 'text', 'ref', 'push')", pyRepr(a.mode))
	}
	sub, _, err := ciParseArgs(rest[1:], subSpec, false)
	if err != nil {
		return nil, err
	}
	// argparse copies the subparser namespace over the main one: a --terms-file given after the mode replaces the
	// ones given before it (default=SUPPRESS keeps an absent option from overwriting)
	a.termFiles = main.all("--terms-file")
	if sub.has("--terms-file") {
		a.termFiles = sub.all("--terms-file")
	}
	a.noTerms = main.has("--no-terms") || sub.has("--no-terms")
	switch a.mode {
	case "text":
		if len(sub.pos) > 1 {
			return nil, errors.New("unrecognized arguments: " + strings.Join(sub.pos[1:], " "))
		}
		if len(sub.pos) == 1 {
			a.file = sub.pos[0]
		}
	case "ref":
		if len(sub.pos) == 0 {
			return nil, errors.New("the following arguments are required: NAME")
		}
		a.names = sub.pos
	case "push":
		if len(sub.pos) < 2 {
			return nil, errors.New("the following arguments are required: base, head")
		}
		if len(sub.pos) > 2 {
			return nil, errors.New("unrecognized arguments: " + strings.Join(sub.pos[2:], " "))
		}
		a.base, a.head = sub.pos[0], sub.pos[1]
		if v, ok := sub.last("--repo"); ok {
			a.repo = v
		}
		a.allowlist, a.hasAllow = sub.last("--allowlist")
	}
	return a, nil
}

// CIPublicTextGuardMain is ci/public_text_guard.py main(): args without the program name.
func CIPublicTextGuardMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return ciPTGMain(args, stdin, stdout, stderr, false)
}

func ciPTGMain(args []string, stdin io.Reader, stdout, stderr io.Writer, secretsOnly bool) int {
	out, errw := &ciOut{w: stdout}, &ciOut{w: stderr}
	code := ciPTGRun(args, stdin, out, errw, secretsOnly)
	if out.err != nil || errw.err != nil {
		if _, err := fmt.Fprintln(os.Stderr, "public-text-guard: error: cannot write output:", errors.Join(out.err, errw.err)); err != nil {
			return 2
		}
		return 2
	}
	return code
}

func ciPTGRun(args []string, stdin io.Reader, out, errw *ciOut, secretsOnly bool) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			out.printf("usage: public_text_guard.py [-h] [--terms-file FILE] [--no-terms] {text,ref,push} ...\n")
			return 0
		}
	}
	code, err := ciPTG(args, stdin, out, errw, secretsOnly)
	if err == nil {
		return code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		msg := ue.Msg
		if !strings.HasPrefix(msg, "public-text-guard:") {
			msg = "public-text-guard: error: " + msg
		}
		errw.printf("%s\n", msg)
		return 2
	}
	errw.printf("public-text-guard: error: %s\n", err.Error())
	return 1
}

func ciPTG(args []string, stdin io.Reader, out, errw *ciOut, secretsOnly bool) (int, error) {
	a, err := ciPTGParse(args)
	if err != nil {
		errw.printf("usage: public_text_guard.py [-h] [--terms-file FILE] [--no-terms] {text,ref,push} ...\npublic_text_guard.py: error: %s\n", err.Error())
		return 2, nil
	}
	var terms []string
	blind := false
	switch {
	case secretsOnly || a.noTerms: // terms optional; in secretsOnly they only mask hit locations, never hit
		files := a.termFiles
		if v := os.Getenv(termsVar); v != "" {
			files = append(append([]string(nil), files...), v)
		}
		if terms, err = ciLoadTerms(files); err != nil && secretsOnly {
			blind, err = true, nil // no terms are needed here, so an unreadable file hides every location instead
		} else if err != nil {
			return 0, err
		}
	default:
		if terms, err = ciTermsOrFail("", a.termFiles); err != nil {
			return 0, err
		}
	}
	if len(terms) > 0 && !secretsOnly {
		errw.printf("public-text-guard: %d owner term(s) loaded\n", len(terms))
	}
	sc := newCIScanner(terms, !secretsOnly) // extraRule holds identity rules only
	sc.secretsOnly, sc.blind = secretsOnly, blind
	var hits []ciHit
	switch a.mode {
	case "text":
		var data []byte
		if a.file == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(a.file)
		}
		if err != nil {
			return 0, &usageError{"cannot read " + pyRepr(a.file) + ": " + pyStrerror(err)}
		}
		for n, line := range pyLines(pyDecodeReplace(data)) {
			for _, p := range sc.scanLine(line) {
				hits = append(hits, ciHit{p.id, "line " + strconv.Itoa(n+1), p.masked})
			}
		}
	case "ref":
		for _, name := range a.names {
			for _, p := range ciCheckText(name, sc) {
				hits = append(hits, ciHit{p.id, "ref:" + sc.hideAll(name), p.masked})
			}
		}
	default:
		allowPath := a.allowlist
		if !a.hasAllow {
			allowPath = ciDefaultAllowlist()
		}
		allow, aerr := ciLoadAllowlist(allowPath, errw.w)
		if aerr != nil {
			return 0, aerr
		}
		if hits, err = ciCheckPush(a.repo, a.base, a.head, sc, allow); err != nil {
			return 0, err
		}
	}
	for _, h := range hits {
		out.printf("%s %s %s\n", h.id, sc.hideTerms(h.loc), h.masked)
	}
	out.printf("%d hits\n", len(hits))
	if len(hits) > 0 {
		return 1, nil
	}
	return 0, nil
}

// ciCheckText is public_text_guard.check_text: every hit of text, line by line.
func ciCheckText(text string, sc *ciScanner) []ciPair {
	var out []ciPair
	for _, line := range pyLines(text) {
		out = append(out, sc.scanLine(line)...)
	}
	return out
}
