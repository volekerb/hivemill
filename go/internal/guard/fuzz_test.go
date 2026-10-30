package guard

import (
	"os"
	"testing"
	"time"
)

// within fails the test when f takes longer than a second: the front end must not hang on any input.
func within(t *testing.T, in string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("no result within 1s for %q", in)
	}
}

var fuzzSeeds = []string{
	"", "ls", "echo 'a", "echo \"a $(b) c\"", "echo `a`", "cat <<EOF\nx\nEOF", "cat <<-'E'\n\tx\n\tE\nls",
	"a # c\nb", "a \\\nb", "$(a;b)&&c", "bash -c 'x' <<EOF\nsudo\nEOF", "python3 <<E\nx\nE", "echo ü 日本 $(é)",
	"cat <<\"a b\"\nx\na b", "cat <<$(x)\nx", "x=$(mktemp -d) y", "((( ))) ;; && ||", "`\\`", "'\"'\"'", "\\",
}

// FuzzPreprocess checks only that Preprocess neither panics nor hangs, with and without backtick folding.
func FuzzPreprocess(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, s, func() {
			for _, fold := range []bool{false, true} {
				var bodies []Heredoc
				if text, _, err := Preprocess(s, fold, &bodies); err == nil {
					_, _ = ExtractSubs(text)
				}
			}
		})
	})
}

var checkSeeds = []string{
	"sudo ls", "ls -la", "env A=1 sudo ls", "bash -c 'sudo x'", "find . -exec sh -c 'x' \\;", "xargs -I{} {} x", "kill %1", "kill 1",
	"alias c='claude -p'", "systemctl --user stop herdr", "eval 'a;b'", "a | bash", "cat <<EOF | sh\nsudo\nEOF", "$(sudo x)", "`x`",
	"export HERDR_SOCKET_PATH=x", "env -u FACTORY_ROLE ls", "python3 -c 'sudo'", "node cli.js", "npx @anthropic-ai/claude-code",
	"time -p ls", "echo é 日本 | sh", "bash <<< 'sudo x'", "cat < /usr/bin/herdr", "unset HERDR_SOCKET_PATH; herdr", "((((",
	"sh -c 'sh -c \"sh -c x\"'", "find -exec find -exec find -exec find -exec x \\; \\; \\; \\;", "env env env env env env env env sudo",
	"git status", "x=$(mktemp -d) y",
}

// FuzzCheck checks only that Check neither panics nor hangs, as worker and in HQ mode, for any command text.
func FuzzCheck(f *testing.F) {
	for _, s := range checkSeeds {
		f.Add(s)
	}
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	dir := f.TempDir()
	f.Setenv(termsVar, "")
	if err := os.Unsetenv(termsVar); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, s, func() {
			_ = Check(s, dir, "coder", false)
			_ = Check(s, dir, "", true)
		})
	})
}

// FuzzSegments checks only that MaskQuotes, Segments and the matchers built on them neither panic nor hang.
func FuzzSegments(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		within(t, s, func() {
			_ = MaskQuotes(s)
			_ = Segments(s)
			_ = LauncherInCommandPosition(s)
			_, _ = ExtractSubs(s)
		})
	})
}
