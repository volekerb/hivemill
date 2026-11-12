package guard

import "strings"

// matchR2Set is a port of R2_SET.search(s) (worker-guard.py:576-579):
//
//	\b(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\s*\+?=
//	|\bexport\s+(?:-\w+\s+)*(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b
//	|\b(?:declare|typeset)\s+-\w*x\w*\s+(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b
func matchR2Set(s string) bool {
	for i := 0; i < len(s); i++ {
		if !boundary(s, i) || wordBefore(s, i) {
			continue // \b before a word character needs a non-word character (or the start) in front
		}
		if j, ok := herdrVarEnd(s, i); ok {
			k := skipSpace(s, j)
			if hasAt(s, k, "+") {
				k++
			}
			if hasAt(s, k, "=") {
				return true
			}
		}
		if hasAt(s, i, "export") {
			j := skipSpace(s, i+6)
			if j > i+6 && exportTail(s, j) {
				return true
			}
		}
		for _, kw := range []string{"declare", "typeset"} {
			if hasAt(s, i, kw) {
				j := skipSpace(s, i+len(kw))
				if j > i+len(kw) && declareTail(s, j) {
					return true
				}
			}
		}
	}
	return false
}

// herdrVarEnd is the end of HERDR_SOCKET_PATH, HERDR_SESSION or HC_SOCKET at i (no trailing \b check).
func herdrVarEnd(s string, i int) (int, bool) {
	for _, v := range []string{"HERDR_SOCKET_PATH", "HERDR_SESSION", "HC_SOCKET"} {
		if hasAt(s, i, v) {
			return i + len(v), true
		}
	}
	return 0, false
}

func socketVarAt(s string, p int) bool {
	for _, v := range []string{"HERDR_SOCKET_PATH", "HC_SOCKET"} {
		if hasAt(s, p, v) && boundary(s, p) && boundary(s, p+len(v)) && !wordBefore(s, p) {
			return true
		}
	}
	return false
}

// exportTail matches (?:-\w+\s+)*HERDR_(?:SOCKET_PATH|SESSION)\b at i.
func exportTail(s string, i int) bool {
	for {
		if j, ok := herdrVarEnd(s, i); ok && boundary(s, j) {
			return true
		}
		if !hasAt(s, i, "-") {
			return false
		}
		j := i + 1
		for j < len(s) && wordAt(s, j) {
			j += runeLen(s, j)
		}
		if j == i+1 {
			return false
		}
		k := skipSpace(s, j)
		if k == j {
			return false
		}
		i = k
	}
}

// declareTail matches -\w*x\w*\s+HERDR_(?:SOCKET_PATH|SESSION)\b at i.
func declareTail(s string, i int) bool {
	if !hasAt(s, i, "-") {
		return false
	}
	j := i + 1
	for j < len(s) && wordAt(s, j) {
		j += runeLen(s, j)
	}
	if !strings.Contains(s[i+1:j], "x") {
		return false
	}
	k := skipSpace(s, j)
	if k == j {
		return false
	}
	e, ok := herdrVarEnd(s, k)
	return ok && boundary(s, e)
}

// matchR2SetShell is R2_SET_SHELL.search(s): export/declare/typeset forms only (worker-guard.py:R2_SET_SHELL).
func matchR2SetShell(s string) bool {
	for i := 0; i < len(s); i++ {
		if !boundary(s, i) || wordBefore(s, i) {
			continue
		}
		if hasAt(s, i, "export") {
			j := skipSpace(s, i+6)
			if j > i+6 && exportTail(s, j) {
				return true
			}
		}
		for _, kw := range []string{"declare", "typeset"} {
			if hasAt(s, i, kw) {
				j := skipSpace(s, i+len(kw))
				if j > i+len(kw) && declareTail(s, j) {
					return true
				}
			}
		}
	}
	return false
}

// matchR2SetVar is R2_SET_VAR.search(s) (worker-guard.py:R2_SET_VAR).
func matchR2SetVar(s string) bool {
	for i := 0; i < len(s); i++ {
		if !boundary(s, i) || wordBefore(s, i) {
			continue
		}
		if j, ok := herdrVarEnd(s, i); ok {
			k := skipSpace(s, j)
			if hasAt(s, k, "+") {
				k++
			}
			if hasAt(s, k, "=") {
				return true
			}
		}
	}
	return false
}

// matchR2Unset is a port of R2_UNSET.search(s) (worker-guard.py:266):
//
//	\bunset\b[^\n]*\bHERDR_SOCKET_PATH\b
//	|\benv\b[^\n]*\s(?:-u\s*HERDR_SOCKET_PATH|-[A-Za-z]*i[A-Za-z]*|--ignore-environment|--unset[= ]HERDR_SOCKET_PATH)(?:\s|$)
func matchR2Unset(s string) bool {
	for i := 0; i < len(s); i++ {
		if wordBefore(s, i) {
			continue
		}
		if hasAt(s, i, "unset") && boundary(s, i+5) {
			eol := len(s)
			if k := strings.IndexByte(s[i+5:], '\n'); k >= 0 {
				eol = i + 5 + k
			}
			for p := i + 5; p < eol; p++ {
				if socketVarAt(s, p) {
					return true
				}
			}
		}
		if hasAt(s, i, "env") && boundary(s, i+3) {
			// [^\n]*\s: the \s is any whitespace up to and including the first newline
			for p := i + 3; p < len(s); {
				r := nextRune(s, p)
				if pyIsSpace(r) && envOption(s, p+runeLen(s, p)) {
					return true
				}
				if r == '\n' {
					break
				}
				p += runeLen(s, p)
			}
		}
	}
	return false
}

// envOption matches the option alternatives of R2_UNSET's env branch at i, followed by whitespace or the end.
func envOption(s string, i int) bool {
	tail := func(j int) bool { return j == len(s) || isSpaceAt(s, j) }
	switch {
	case hasAt(s, i, "-u"):
		j := skipSpace(s, i+2)
		for _, v := range []string{"HERDR_SOCKET_PATH", "HC_SOCKET"} {
			if hasAt(s, j, v) && tail(j+len(v)) {
				return true
			}
		}
	case hasAt(s, i, "--ignore-environment"):
		if tail(i + len("--ignore-environment")) {
			return true
		}
	case hasAt(s, i, "--unset"):
		j := i + len("--unset")
		if j < len(s) && (s[j] == '=' || s[j] == ' ') {
			j++
			for _, v := range []string{"HERDR_SOCKET_PATH", "HC_SOCKET"} {
				if hasAt(s, j, v) && tail(j+len(v)) {
					return true
				}
			}
		}
	}
	if hasAt(s, i, "-") {
		j := i + 1
		for j < len(s) && (s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z') {
			j++
		}
		if strings.Contains(s[i+1:j], "i") && tail(j) {
			return true
		}
	}
	return false
}

// Check is a port of worker-guard.py:check. A nil result is exit 0; any error is a block (exit 2), see Explain.
func Check(cmd, cwd, role string, hq bool) error {
	pt, err := newPublicText(cwd, cmd) // one PublicText for both passes: its directories and heredoc bodies carry over
	if err != nil {
		return err
	}
	pt.nopush = in(noPushRoles, role) && !hq // T-0961
	// PRIV pass (the whole of HQ mode): parse trouble allows
	an, err := NewAnalyzer(cwd, role, true)
	if err != nil {
		return err
	}
	an.unitRule = hq
	if hq {
		an.pt = pt
	}
	if err := an.script(cmd, 0); err != nil {
		switch {
		case IsPrivBlock(err):
			if !hq {
				if cerr := claudeOverPriv(cmd, cwd, role); cerr != nil {
					return cerr
				}
			}
			return err
		case IsDelegate(err) || isPublicBlock(err):
			return err
		default:
			// a plain Block. The one HQ rule that blocks on parse trouble is the public-text rule: a command that may be
			// a git or gh write and cannot be parsed is refused when the rule is on for the repo it would write to.
			if hq && ptWriteHint(cmd) {
				if perr := pt.loadConfig(); perr != nil {
					return perr
				}
				if pt.active {
					slug := pt.remoteSlug(cwd, cwd != "", "origin", false)
					if ferr := pt.frozenHit(slug, "origin", true); ferr != nil {
						return ferr
					}
					if pt.applies(slug, "origin", true) {
						return pubBlock("a write command to a public repo cannot be parsed; use plain git/gh commands")
					}
				}
			}
		}
	}
	if hq {
		return nil
	}
	if IsTermsRole(role) {
		if err := TermsBash(cmd, cwd); err != nil {
			return err
		}
	}
	if role == "evaluator" {
		if err := evaluatorHomeText(cmd); err != nil {
			return err
		}
	}
	if role == researcherRole {
		if err := researcherText(cmd); err != nil {
			return err
		}
	}
	// layer 2: raw text, independent of the parser
	if R2SetHit(cmd) || R2PathHit(cmd) {
		return blockf("R2: re-targeting the herdr socket/session or naming herdr's socket/config path")
	}
	if matchR2Unset(cmd) && HasHerdrWord(cmd) {
		return blockf("R2: unsetting HERDR_SOCKET_PATH in a command that mentions herdr")
	}
	if MatchR3Redir(cmd) {
		return blockf("R3: reading the herdr binary through a redirection")
	}
	lines := pyLines(strings.ReplaceAll(cmd, "\\\n", " "))
	if len(lines) == 0 {
		lines = []string{cmd}
	}
	for _, line := range lines {
		if MatchR1(line) && LauncherInCommandPosition(line) {
			return blockf("R1: herdr with a mutating subcommand behind an exec-capable launcher")
		}
	}
	an2, err := NewAnalyzer(cwd, role, false)
	if err != nil {
		return err
	}
	an2.pt = pt
	if err := an2.script(cmd, 0); err != nil {
		if IsPrivBlock(err) {
			if cerr := claudeOverPriv(cmd, cwd, role); cerr != nil {
				return cerr
			}
		}
		return err
	}
	if an2.scrubWithFactory != "" && an2.ranFactory {
		return blockf(an2.scrubWithFactory + " in a command line that runs factory (hides the worker from detection)")
	}
	return nil
}
