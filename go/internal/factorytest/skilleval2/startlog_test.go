package skilleval2

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/sandbox"
	"factory/go/internal/skilleval"
)

// Port of tests/test_skilleval_startlog.py (17 cases; see ported_skilleval2.txt).
//
// The Python shim (nsbox.write_shim / shim_main) and the evaluator spawn are not ported to Go yet (Builder.WriteShim is
// nil until P4-L), so the cases that run the shim are skipped stubs named TestStartlogNotPorted...; the start.log
// writer that exists in Go (sandbox.StartLogAppend) carries the redaction, cap and containment cases.

func startlogNotPorted(t *testing.T, why string) {
	t.Helper()
	t.Skip("not ported: " + why)
}

const noShim = "the evaluator shim (nsbox.write_shim/shim_main) has no Go counterpart before P4-L (skilleval.Builder.WriteShim)"

func TestStartlogNotPortedMissingTokenFileIsLoggedAndExitsNonZero(t *testing.T) {
	startlogNotPorted(t, noShim)
}
func TestStartlogNotPortedUnreadableTokenFileIsLoggedAndExitsNonZero(t *testing.T) {
	startlogNotPorted(t, noShim)
}
func TestStartlogNotPortedStartProxyFailureIsLoggedWithItsMessage(t *testing.T) {
	startlogNotPorted(t, noShim)
}
func TestStartlogNotPortedSuccessfulPreExecPhaseLogsStage1ExecWithoutEnv(t *testing.T) {
	startlogNotPorted(t, noShim)
}
func TestStartlogNotPortedStage1RcAndStderrReachTheTrialLog(t *testing.T) {
	startlogNotPorted(t, noShim+"; Python also needs user namespaces")
}

// startlogDir is a trial dir under a fresh temp dir.
func startlogDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "trials", "tr0001")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func startlogRead(t *testing.T, trial string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(trial, "log", "start.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// RedactionTests: the whole token never reaches start.log, not even inside an error message.
func TestStartlogTokenInAnExceptionMessageIsRedacted(t *testing.T) {
	trial := startlogDir(t)
	msg := "FileNotFoundError: [Errno 2] No such file or directory: '/nonexistent/" + startlogToken + "/py'\n"
	sandbox.StartLogAppend(trial, msg, startlogToken, startlogCap)
	text := startlogRead(t, trial)
	if !strings.Contains(text, "FileNotFoundError") || strings.Count(text, startlogToken) != 0 {
		t.Fatalf("log %q", text)
	}
	if !strings.Contains(text, "[redacted-token]") {
		t.Fatalf("no redaction marker in %q", text)
	}
}

// CapTests: start.log stays <= 64 KiB after 1 MiB of writes.
func TestStartlogLogIsCappedAt64KiBAfter1MiBOfFailures(t *testing.T) {
	trial := startlogDir(t)
	rec := "FileNotFoundError: /nonexistent/" + strings.Repeat("a", 200) + "\n"
	rec = strings.Repeat(rec, 18) // ~3.6 KB per failure
	for i := 0; i < 400; i++ {    // ~1.4 MiB in all
		sandbox.StartLogAppend(trial, rec, startlogToken, startlogCap)
	}
	st, err := os.Stat(filepath.Join(trial, "log", "start.log"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() <= 0 || st.Size() > startlogCap {
		t.Fatalf("start.log is %d bytes", st.Size())
	}
}

// ContainmentTests: the log is <trial>/log/start.log and nothing is written outside the trial dir.
func TestStartlogFailingWriteOnlyTouchesTheTrialDir(t *testing.T) {
	base := t.TempDir()
	trial := filepath.Join(base, "trials", "tr0001")
	for _, d := range []string{trial, filepath.Join(base, "home"), filepath.Join(base, "cwd"), filepath.Join(base, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() map[string]string {
		out := map[string]string{}
		err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if p == trial || strings.HasPrefix(p, trial+string(filepath.Separator)) {
				return nil
			}
			out[p] = fmt.Sprintf("%v %d %d", info.IsDir(), info.Size(), info.ModTime().UnixNano())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snap()
	sandbox.StartLogAppend(trial, "Traceback\nOSError: no such token\n", startlogToken, startlogCap)
	if !strings.Contains(startlogRead(t, trial), "OSError") {
		t.Fatal("log not written")
	}
	after := snap()
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("outside the trial dir changed:\n%v\n%v", before, after)
	}
	for _, d := range []string{"home", "cwd", "tmp"} {
		if ents, _ := os.ReadDir(filepath.Join(base, d)); len(ents) != 0 {
			t.Fatalf("%s was written to", d)
		}
	}
}

// ---- SkillevalOutputTests: `factory skilleval results|list` ----

var startlogLineRE = regexp.MustCompile(`start-line-\d\d`)

func TestStartlogErrorAndLostTrialsShowLastRcAndAtMost20TailLines(t *testing.T) {
	w := newStartlogWorld(t)
	var body []string
	for i := 1; i <= 40; i++ {
		body = append(body, fmt.Sprintf("start-line-%02d", i))
	}
	w.trial("error", "fix-bug", -1, body, 3)
	for _, out := range w.outputs() {
		for _, want := range []string{"rc=3", "start-line-40"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing in %q", want, out)
			}
		}
		for _, bad := range []string{"start-line-01", "start-line-10"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%q must not be in %q", bad, out)
			}
		}
		if n := len(startlogLineRE.FindAllString(out, -1)); n < 1 || n > 20 {
			t.Fatalf("%d tail lines in %q", n, out)
		}
	}
}

func TestStartlogTokenShapeJoinedByAStrippedControlIsRedactedInResultsAndList(t *testing.T) {
	w := newStartlogWorld(t)
	rest := strings.Repeat("A", 30)
	// \x00 and \x9b are stripped; \x1c and \x0b are line breaks for Python str.splitlines, so they must not split the token
	w.trial("error", "fix-bug", -1, []string{"boom sk-ant-\x00" + rest, "x sk-ant-\x1c" + rest, "y sk-ant-\x0b" + rest, "z sk-ant-\u009b" + rest}, 3)
	for _, out := range w.outputs() {
		if strings.Contains(out, "sk-ant-") || strings.Contains(out, rest) || !strings.Contains(out, "[redacted-token]") {
			t.Fatalf("not redacted: %q", out)
		}
	}
}

func TestStartlogReplacementAndFormatCharsInsideATokenAreStrippedInResultsAndList(t *testing.T) {
	w := newStartlogWorld(t)
	rest := strings.Repeat("A", 30)
	d := w.trial("error", "fix-bug", -1, []string{}, 3)
	raw := "boom sk-ant-\xff" + rest + "\nx sk-ant-\xe2\x80\x8b" + rest + "\ny sk-ant-\xef\xbb\xbf" + rest +
		"\nz sk-ant-\xe2\x81\xa0" + rest + "\ntab\there\nrc=3\n"
	if err := os.WriteFile(filepath.Join(d, "log", "start.log"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, out := range w.outputs() {
		if strings.Contains(out, "sk-ant-") || strings.Contains(out, rest) || strings.Contains(out, "�") ||
			strings.Contains(out, "​") || !strings.Contains(out, "[redacted-token]") || !strings.Contains(out, "tab\there") {
			t.Fatalf("bad output %q", out)
		}
	}
}

func TestStartlogC1ControlsAreNotDisplayed(t *testing.T) {
	w := newStartlogWorld(t)
	w.trial("error", "fix-bug", -1, []string{"a\u009bb\u0085c\u0080d\u009fe"}, 3)
	for _, out := range w.outputs() {
		if !strings.Contains(out, "abcde") {
			t.Fatalf("abcde missing in %q", out)
		}
		for _, ch := range []string{"\u009b", "\u0085", "\u0080", "\u009f"} {
			if strings.Contains(out, ch) {
				t.Fatalf("%q displayed in %q", ch, out)
			}
		}
	}
}

// The Python test spies on nsbox._TOKEN_SHAPE.sub to prove redaction runs on the 20 displayed lines only. Go has no
// such seam; the observable contract is that exactly the last 20 lines are shown (and 100 lines in, 20 out).
func TestStartlogRedactionRunsOnlyOnTheDisplayedLines(t *testing.T) {
	trial := startlogDir(t)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "line-%03d\n", i)
	}
	sandbox.StartLogAppend(trial, b.String(), "", startlogCap)
	row := skilleval.Trial{Status: "error", Sandbox: nullS(trial)}
	got := skilleval.StartLogLines(row, 20)
	if len(got) != 21 || got[0] != "  start.log:" || got[1] != "    line-080" {
		t.Fatalf("got %q", got)
	}
	if got[len(got)-1] != "    line-099" {
		t.Fatalf("last %q", got[len(got)-1])
	}
}

func TestStartlogRunningWithoutTokensShowsTheLogButRunningWithTokensDoesNot(t *testing.T) {
	w := newStartlogWorld(t)
	w.trial("running", "fix-bug", -1, []string{"stuck-without-tokens-mark"}, 137)
	w.trial("running", "add-feature", 500, []string{"healthy-running-mark"}, 0)
	for _, out := range w.outputs() {
		for _, want := range []string{"stuck-without-tokens-mark", "rc=137"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing in %q", want, out)
			}
		}
		if strings.Contains(out, "healthy-running-mark") {
			t.Fatalf("healthy running trial shows its log: %q", out)
		}
	}
}

func TestStartlogPassedTrialShowsNoExtraLinesAndAnErrorTrialWithoutALogStillLists(t *testing.T) {
	w := newStartlogWorld(t)
	w.trial("passed", "fix-bug", 100, []string{"passed-log-mark"}, 0)
	w.trial("error", "add-feature", -1, []string{"error-log-mark"}, 3)
	w.trial("error", "no-log-task", -1, nil, 0) // no start.log at all: listed as before, no crash
	for _, out := range w.outputs() {
		for _, want := range []string{"trial 1 status=passed", "trial 3 status=error", "error-log-mark"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing in %q", want, out)
			}
		}
		if strings.Contains(out, "passed-log-mark") {
			t.Fatalf("passed trial shows its log: %q", out)
		}
		if n := strings.Count(out, "rc="); n != 1 {
			t.Fatalf("%d rc= in %q, want 1", n, out)
		}
	}
}

func TestStartlogTheTokenNeverAppearsInResultsOrList(t *testing.T) {
	w := newStartlogWorld(t)
	w.trial("error", "fix-bug", -1, []string{"before", "claude failed: bad token " + startlogToken + " rejected", "after"}, 1)
	for _, out := range w.outputs() {
		if !strings.Contains(out, "rc=1") || !strings.Contains(out, "after") {
			t.Fatalf("log not shown: %q", out)
		}
		if strings.Count(out, startlogToken) != 0 || strings.Contains(out, "oat01-STARTLOGDUMMYTOKEN") {
			t.Fatalf("token leaked: %q", out)
		}
	}
}

func TestStartlogShowingResultsWritesNothingIntoTheTrialDir(t *testing.T) {
	w := newStartlogWorld(t)
	d := w.trial("error", "fix-bug", -1, []string{"x"}, 3)
	snap := func() map[string]time.Time {
		out := map[string]time.Time{}
		err := filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				out[p] = info.ModTime()
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snap()
	for _, out := range w.outputs() {
		if !strings.Contains(out, "rc=3") {
			t.Fatalf("rc=3 missing in %q", out)
		}
	}
	after := snap()
	if len(before) != len(after) {
		t.Fatalf("files changed: %v -> %v", before, after)
	}
	for p, m := range before {
		if !after[p].Equal(m) {
			t.Fatalf("%s mtime changed", p)
		}
	}
}
