package guard

import (
	"strings"
	"unicode/utf8"
)

// T-0882 (worker-guard.py:stdin_program, stdin_scan): NO_PUSH_ROLES may not feed an interpreter program on stdin
// (heredoc, here-string, echo/printf pipe, cat heredoc pipe) that launches git push or a gh write, or (python)
// extracts an archive.

// stdinChannel is worker-guard.py:Analyzer.cmd's stdin= tuple: piped flag, here-string/heredoc bodies, echo|printf|cat literal.
type stdinChannel struct {
	piped bool
	hs    []string
	pipe  string
}

func (a *Analyzer) activeStdin(stdin *stdinChannel, piped bool, hstrs []string) stdinChannel {
	if stdin != nil {
		return *stdin
	}
	return stdinChannel{piped: piped, hs: hstrs, pipe: a.pipeSrc}
}

// stdinProgram is a port of Analyzer.stdin_program.
func stdinProgram(name string, rest []string) bool {
	if !inlineCodeInterp(name) {
		return false
	}
	iname := name
	if strings.HasPrefix(name, "nodejs") {
		iname = "node"
	}
	si, ci, unknown := InterpArgs(iname, rest)
	if unknown {
		return true
	}
	if ci >= 0 {
		return false
	}
	if si >= 0 && rePTStdinPath.MatchString(ptNormpath(rest[si])) {
		return true
	}
	return si < 0 || rest[si] == "-" || rePTStdinPath.MatchString(ptNormpath(rest[si]))
}

// scanInlineText is a port of Analyzer.scan_inline_text (T-0789 / T-0882).
func (a *Analyzer) scanInlineText(name, t, what string) error {
	if hasWordToken(t, "git") && hasWordToken(t, "push") {
		return blockf(name + " " + what + " that runs `git push` (no-push roles never push; ask your lead)")
	}
	if matchInlineGhWrite(t) || inlineGhAPIBody(t) {
		return blockf(name + " " + what + " that runs a gh write (pr merge/create, repo, api with a write method or body, release write); " +
			"no-push roles never write to GitHub; ask your lead")
	}
	return nil
}

// stdinExtractArchive matches worker-guard.py stdin_scan's re.search(r"extractall|extract\s*\(|unpack_archive", text).
func stdinExtractArchive(text string) bool {
	if strings.Contains(text, "extractall") || strings.Contains(text, "unpack_archive") {
		return true
	}
	const needle = "extract"
	for i := 0; i <= len(text)-len(needle); i++ {
		if text[i:i+len(needle)] != needle {
			continue
		}
		p := i + len(needle)
		for p < len(text) {
			r, w := utf8.DecodeRuneInString(text[p:])
			if r == '(' {
				return true
			}
			if !pyIsSpace(r) {
				break
			}
			p += w
		}
	}
	return false
}

// stdinScan is a port of Analyzer.stdin_scan.
func (a *Analyzer) stdinScan(name, text string) error {
	if err := a.scanInlineText(name, text, "stdin code"); err != nil {
		return err
	}
	if isPythonName(name) && stdinExtractArchive(text) {
		return blockf("`" + name + "` stdin code that extracts an archive cannot be bounded (it may write .git/config or hooks); " +
			"use unzip -d or tar -C")
	}
	return nil
}
