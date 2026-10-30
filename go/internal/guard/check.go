package guard

import "strings"

// matchR2Set is a port of R2_SET.search(s) (worker-guard.py:258-259):
//
//	\bHERDR_(?:SOCKET_PATH|SESSION)\s*\+?=
//	|\bexport\s+(?:-\w+\s+)*HERDR_(?:SOCKET_PATH|SESSION)\b
//	|\b(?:declare|typeset)\s+-\w*x\w*\s+HERDR_(?:SOCKET_PATH|SESSION)\b
func matchR2Set(s string) bool {
	for i := 0; i < len(s); i++ {
		if !boundary(s, i) || wordBefore(s, i) {
			continue // \b before a word character needs a non-word character (or the start) in front
		}
		if hasAt(s, i, "HERDR_") {
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

// herdrVarEnd is the end of HERDR_SOCKET_PATH or HERDR_SESSION at i (no trailing \b check).
func herdrVarEnd(s string, i int) (int, bool) {
	for _, v := range []string{"HERDR_SOCKET_PATH", "HERDR_SESSION"} {
		if hasAt(s, i, v) {
			return i + len(v), true
		}
	}
	return 0, false
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
			for p := i + 5; p+len("HERDR_SOCKET_PATH") <= eol; p++ {
				if hasAt(s, p, "HERDR_SOCKET_PATH") && boundary(s, p) && boundary(s, p+len("HERDR_SOCKET_PATH")) && !wordBefore(s, p) {
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
		if hasAt(s, j, "HERDR_SOCKET_PATH") && tail(j+len("HERDR_SOCKET_PATH")) {
			return true
		}
	case hasAt(s, i, "--ignore-environment"):
		if tail(i + len("--ignore-environment")) {
			return true
		}
	case hasAt(s, i, "--unset"):
		j := i + len("--unset")
		if j < len(s) && (s[j] == '=' || s[j] == ' ') {
			j++
			if hasAt(s, j, "HERDR_SOCKET_PATH") && tail(j+len("HERDR_SOCKET_PATH")) {
				return true
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
	// PRIV pass (the whole of HQ mode): parse trouble allows
	an := NewAnalyzer(cwd, role, true)
	an.unitRule = hq
	if err := an.script(cmd, 0); err != nil {
		switch {
		case IsPrivBlock(err):
			if !hq {
				if cerr := claudeOverPriv(cmd, cwd, role); cerr != nil {
					return cerr
				}
			}
			return err
		case IsNotPorted(err):
			return err
		default:
			// a plain Block. The one HQ rule that blocks on parse trouble is the public-text rule, which reads the
			// factory DB to know whether it is on: stay closed for any command that may be a git or gh write.
			if hq && (strings.Contains(cmd, "git") || strings.Contains(cmd, "gh")) {
				return notPorted("public-text write hint on a command that cannot be parsed (T6d)")
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
	// layer 2: raw text, independent of the parser
	if matchR2Set(cmd) || R2PathHit(cmd) {
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
	an2 := NewAnalyzer(cwd, role, false)
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
