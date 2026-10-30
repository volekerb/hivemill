package guard

import (
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
