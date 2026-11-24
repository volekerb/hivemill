package skilleval2

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/sandbox"
	"factory/go/internal/skilleval"
)

// Port of tests/test_skilleval_startlog_safety.py (17 cases; see ported_skilleval2.txt).
//
// start.log is written outside the sandbox into a dir the sandboxed claude can write, so it must never follow a
// planted symlink. The writer (sandbox.StartLogAppend) and the reader (skilleval.StartLogLines) exist in Go and are
// tested here. The shim signal/stage-1 cases and the stderr tee have no Go counterpart before P4-L: skipped stubs.

const slsafeNoShim = "the evaluator shim (nsbox.shim_main) has no Go counterpart before P4-L (skilleval.Builder.WriteShim)"
const slsafeNoTee = "nsbox._tee_stderr has no Go counterpart before P4-L"

func TestStartlogSafetyNotPortedSigtermDuringStartStopsTheShimBeforeStage1(t *testing.T) {
	t.Skip("not ported: " + slsafeNoShim)
}
func TestStartlogSafetyNotPortedSighupDuringStartStopsTheShimBeforeStage1(t *testing.T) {
	t.Skip("not ported: " + slsafeNoShim)
}
func TestStartlogSafetyNotPortedSigtermDuringReadTokenNeverStartsTheProxy(t *testing.T) {
	t.Skip("not ported: " + slsafeNoShim)
}
func TestStartlogSafetyNotPortedQuitUsr1Usr2ReachStage1(t *testing.T) {
	t.Skip("not ported: " + slsafeNoShim)
}
func TestStartlogSafetyNotPortedAKilledShimLeavesNoLoopingFakeUnshareBehind(t *testing.T) {
	t.Skip("not ported: " + slsafeNoShim)
}
func TestStartlogSafetyNotPorted50MBWithoutANewlineStaysInBoundedMemory(t *testing.T) {
	t.Skip("not ported: " + slsafeNoTee)
}
func TestStartlogSafetyNotPortedATokenSplitAcrossA4096ByteBoundaryIsRedacted(t *testing.T) {
	t.Skip("not ported: " + slsafeNoTee)
}
func TestStartlogSafetyNotPortedTheExactTokenSplitAtEveryOffsetIsAbsent(t *testing.T) {
	t.Skip("not ported: " + slsafeNoTee)
}
func TestStartlogSafetyNotPortedEndlessTokenShapedInputIsLinearInTimeAndMemory(t *testing.T) {
	t.Skip("not ported: " + slsafeNoTee)
}

// slsafeVictim is VICTIM: 104 kB, larger than the 64 KiB cap.
var slsafeVictim = bytes.Repeat([]byte("VICTIM-BYTES-"), 8000)

func slsafeWorld(t *testing.T) (base, trial, victim string) {
	t.Helper()
	base = t.TempDir()
	trial = filepath.Join(base, "trial")
	victim = filepath.Join(base, "victim.txt")
	if err := os.Mkdir(trial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, slsafeVictim, 0o644); err != nil {
		t.Fatal(err)
	}
	return base, trial, victim
}

func slsafeAppend3(trial string) {
	for i := 0; i < 3; i++ {
		sandbox.StartLogAppend(trial, strings.Repeat("x", 40000)+"\n", "", startlogCap)
	}
}

func slsafeRow(trial string) skilleval.Trial {
	return skilleval.Trial{Status: "error", Sandbox: nullS(trial)}
}

func TestStartlogSafetyStartLogSymlinkToAHostFileIsNotFollowed(t *testing.T) {
	_, trial, victim := slsafeWorld(t)
	if err := os.Mkdir(filepath.Join(trial, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(trial, "log", "start.log")); err != nil {
		t.Fatal(err)
	}
	slsafeAppend3(trial)
	if got, _ := os.ReadFile(victim); !bytes.Equal(got, slsafeVictim) {
		t.Fatalf("victim changed (%d bytes)", len(got))
	}
}

func TestStartlogSafetyLogDirSymlinkToAHostDirIsNotFollowed(t *testing.T) {
	base, trial, _ := slsafeWorld(t)
	vdir := filepath.Join(base, "vdir")
	if err := os.Mkdir(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "start.log"), slsafeVictim, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vdir, filepath.Join(trial, "log")); err != nil {
		t.Fatal(err)
	}
	slsafeAppend3(trial)
	if got, _ := os.ReadFile(filepath.Join(vdir, "start.log")); !bytes.Equal(got, slsafeVictim) {
		t.Fatalf("victim changed (%d bytes)", len(got))
	}
	ents, err := os.ReadDir(vdir)
	if err != nil || len(ents) != 1 || ents[0].Name() != "start.log" {
		t.Fatalf("vdir listing %v %v", ents, err)
	}
}

func TestStartlogSafetyRegularLogIsStillWrittenWith0600AndCapped(t *testing.T) {
	_, trial, _ := slsafeWorld(t)
	for i := 0; i < 5; i++ {
		sandbox.StartLogAppend(trial, strings.Repeat("y", 30000)+"\n", "", startlogCap)
	}
	st, err := os.Stat(filepath.Join(trial, "log", "start.log"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() <= 0 || st.Size() > startlogCap || st.Mode().Perm() != 0o600 {
		t.Fatalf("size %d mode %o", st.Size(), st.Mode().Perm())
	}
}

func TestStartlogSafetyFifoStartLogIsRefusedWithoutBlocking(t *testing.T) {
	_, trial, _ := slsafeWorld(t)
	if err := os.Mkdir(filepath.Join(trial, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(trial, "log", "start.log"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- sandbox.StartLogAppend(trial, "a", "", startlogCap) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a write to a FIFO start.log must be refused")
		}
	case <-time.After(20 * time.Second): // Python: subprocess timeout=20
		t.Fatal("StartLogAppend blocked on a FIFO")
	}
}

func TestStartlogSafetyReaderDoesNotShowASymlinkedLogOrLogDir(t *testing.T) {
	base, trial, _ := slsafeWorld(t)
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("HOST-SECRET-CONTENT\nrc=9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := slsafeRow(trial)
	log := filepath.Join(trial, "log")
	if err := os.Mkdir(log, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(log, "start.log")); err != nil {
		t.Fatal(err)
	}
	if got := skilleval.StartLogLines(row, 20); len(got) != 0 {
		t.Fatalf("symlinked log shown: %q", got)
	}
	if err := os.Remove(filepath.Join(log, "start.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	vdir := filepath.Join(base, "vdir")
	if err := os.Mkdir(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "start.log"), []byte("HOST-SECRET-CONTENT\nrc=9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vdir, log); err != nil {
		t.Fatal(err)
	}
	if got := skilleval.StartLogLines(row, 20); len(got) != 0 {
		t.Fatalf("symlinked log dir shown: %q", got)
	}
}

func TestStartlogSafetyReaderStillShowsARegularLog(t *testing.T) {
	_, trial, _ := slsafeWorld(t)
	sandbox.StartLogAppend(trial, "hello rc=4\n", "", startlogCap)
	got := skilleval.StartLogLines(slsafeRow(trial), 20)
	found := false
	for _, l := range got {
		found = found || l == "    hello rc=4"
	}
	if !found {
		t.Fatalf("got %q", got)
	}
}

func TestStartlogSafetyEscapeAndC0CharactersAreStrippedFromTheDisplayedLog(t *testing.T) {
	trial := t.TempDir()
	sandbox.StartLogAppend(trial, "a\x1b[2Jb\x07c\td\x00e\rrc=2\n", "", startlogCap)
	out := strings.Join(skilleval.StartLogLines(slsafeRow(trial), 20), "\n")
	for _, bad := range []string{"\x1b", "\x07", "\x00", "\r"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q displayed in %q", bad, out)
		}
	}
	if !strings.Contains(out, "a[2Jbc\tde") {
		t.Fatalf("%q", out)
	}
}

func TestStartlogSafetyTokenShapeSplitByAControlCharacterIsStillRedacted(t *testing.T) {
	trial := t.TempDir()
	sandbox.StartLogAppend(trial, "boom sk-ant-\x1bAAAAAAAAAAAAAAAAAAAAAAAA tail\nrc=2\n", "", startlogCap)
	out := strings.Join(skilleval.StartLogLines(slsafeRow(trial), 20), "\n")
	if strings.Contains(out, "sk-ant-") || !strings.Contains(out, "[redacted-token]") {
		t.Fatalf("%q", out)
	}
}
