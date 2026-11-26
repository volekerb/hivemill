package hm

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// PR body rules of docs/pr-descriptions.md (the same rules `factory prcheck` applies).
var (
	PRHeadings     = []string{"## Summary", "## Why", "## Changes", "## Test evidence", "## Risk / rollback"}
	acceptanceWalk = "## Acceptance walk"
	prWordsMax     = 300
	detailsRe      = regexp.MustCompile(`(?is)<details\b.*?</details>`)
	commentRe      = regexp.MustCompile(`(?s)<!--.*?-->`)
	fenceOpenRe    = regexp.MustCompile("^[ \t]*(`{3,}|~{3,})")
	backtickRe     = regexp.MustCompile("`([^`\n]+)`")
	envPrefixRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
	evidenceOutRe  = regexp.MustCompile(`\bRan \d+|\bOK\b|\bFAILED\b|\b\d+ failed\b|\bpass(?:ed|es)?\b|\brc=\d+|\bexit \d+|->|→`)
	testsRemovedRe = regexp.MustCompile(`(?im)^\W*tests? removed:\s*\S`)
	runners        = map[string]bool{"python3": true, "python": true, "pytest": true, "go": true, "bash": true, "sh": true, "zsh": true,
		"make": true, "git": true, "gh": true, "grep": true, "factory": true, "hivemill": true, "npm": true, "pnpm": true, "node": true,
		"npx": true}
)

// outsideDetails is the body without <details> blocks and HTML comments.
func outsideDetails(body string) string {
	return commentRe.ReplaceAllString(detailsRe.ReplaceAllString(commentRe.ReplaceAllString(body, ""), ""), "")
}

// fenced marks lines inside code fences; unclosed reports a fence that never closes.
func fenced(lines []string) (in []bool, unclosed bool) {
	in = make([]bool, len(lines))
	open := ""
	for i, ln := range lines {
		if open != "" {
			in[i] = true
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, open) && strings.Trim(t, open[:1]) == "" {
				open = ""
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(ln); m != nil {
			rest := strings.TrimSpace(ln)[len(m[1]):]
			if m[1][0] == '`' && strings.Contains(rest, "`") {
				continue // ```x``` on one line is inline code, not a fence
			}
			open, in[i] = m[1], true
		}
	}
	return in, open != ""
}

func isCommand(text string) bool {
	t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text), "$"))
	for _, seg := range regexp.MustCompile(`&&|;`).Split(t, -1) {
		toks := strings.Fields(seg)
		for len(toks) > 0 && envPrefixRe.MatchString(toks[0]) {
			toks = toks[1:]
		}
		// path.Base leaves a bare name as is, so a runner given by path counts too (T-1979).
		if len(toks) >= 2 && (runners[path.Base(toks[0])] || strings.HasPrefix(toks[0], "ci/") || strings.HasPrefix(toks[0], "./")) {
			return true
		}
	}
	return false
}

// BodyProblems lists how a PR body departs from the template; testsDropped adds the `Tests removed:` rule.
func BodyProblems(body string, testsDropped bool) []string {
	var probs []string
	outside := outsideDetails(body)
	lines := strings.Split(strings.ReplaceAll(outside, "\r\n", "\n"), "\n")
	in, unclosed := fenced(lines)
	if unclosed {
		probs = append(probs, "a code fence outside <details> is never closed: add the closing ``` line")
	}
	pos := map[string]int{}
	for i, ln := range lines {
		if !in[i] {
			if _, ok := pos[strings.TrimRight(ln, " \t")]; !ok {
				pos[strings.TrimRight(ln, " \t")] = i
			}
		}
	}
	var missing []string
	prev := -1
	ordered := true
	for _, h := range PRHeadings {
		p, ok := pos[h]
		if !ok {
			missing = append(missing, "`"+h+"`")
			continue
		}
		if p < prev {
			ordered = false
		}
		prev = p
	}
	if len(missing) > 0 {
		probs = append(probs, "missing section heading(s): "+strings.Join(missing, ", "))
	} else if !ordered {
		probs = append(probs, "section headings out of order; the template order is "+strings.Join(PRHeadings, ", "))
	}
	walk := false
	for _, blk := range detailsRe.FindAllString(commentRe.ReplaceAllString(body, ""), -1) {
		bl := strings.Split(blk, "\n")
		for i, ln := range bl {
			if strings.TrimRight(ln, " \t\r") == acceptanceWalk {
				for _, nx := range bl[i+1:] {
					if s := strings.TrimSpace(nx); s != "" && !strings.HasPrefix(s, "</") {
						walk = true
					}
				}
			}
		}
	}
	if !walk {
		probs = append(probs, "no <details> block holding a `## Acceptance walk` line with at least one item under it")
	}
	if n := len(strings.Fields(outside)); n > prWordsMax {
		probs = append(probs, fmt.Sprintf("%d words outside <details>; the limit is %d (target 150-250): move evidence into <details> or cut", n, prWordsMax))
	}
	// ## Test evidence: a command in backticks (or a fenced line) and an output marker
	if p, ok := pos["## Test evidence"]; ok {
		end := len(lines)
		for i := p + 1; i < len(lines); i++ {
			if !in[i] && strings.HasPrefix(lines[i], "## ") {
				end = i
				break
			}
		}
		sec := lines[p+1 : end]
		cmd := false
		for i, ln := range sec {
			if in[p+1+i] && isCommand(ln) {
				cmd = true
			}
			for _, m := range backtickRe.FindAllStringSubmatch(ln, -1) {
				if isCommand(m[1]) {
					cmd = true
				}
			}
		}
		text := strings.Join(sec, "\n")
		if !cmd {
			probs = append(probs, "`## Test evidence` has no command in backticks (2+ words starting with a runner such as python3, go, "+
				"bash, make, git, gh, npm or ./...)")
		} else if !evidenceOutRe.MatchString(text) {
			probs = append(probs, "`## Test evidence` has a command but no output line (Ran N, OK, FAILED, passed, rc=N, exit N, ->)")
		}
	}
	if testsDropped && !testsRemovedRe.MatchString(outside) {
		probs = append(probs, "the test count drops: add a line `Tests removed: <which and why>` outside <details>")
	}
	return probs
}
