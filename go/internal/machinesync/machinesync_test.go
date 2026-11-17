package machinesync

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const marker = "FIXTURE-VALUE-9f2c" // made-up marker, not a secret

// fakeSSH puts an ssh on PATH that runs the remote command locally with HOME set to a temp "remote home".
// FAKE_SSH_FAIL=1 makes the copy command echo its stdin on stderr and exit 1; FAKE_STAT=<mode> overrides stat output.
func fakeSSH(t *testing.T) (localHome, remoteHome string) {
	t.Helper()
	root := t.TempDir()
	localHome = filepath.Join(root, "local")
	remoteHome = filepath.Join(root, "remote")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{localHome, remoteHome, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
if [ -n "$FAKE_SSH_ARGS" ]; then echo "$@" >> "$FAKE_SSH_ARGS"; fi
if [ -n "$FAKE_SSH_HANG" ]; then exec sleep 30; fi
while [ "$1" != "--" ]; do shift; done
shift
cmd="$*"
case "$cmd" in
  *"cat >"*) if [ -n "$FAKE_SSH_FAIL" ]; then cat >&2; echo "permission denied" >&2; exit 1; fi ;;
  "stat -c"*) if [ -n "$FAKE_STAT" ]; then echo "$FAKE_STAT"; exit 0; fi ;;
esac
HOME="$FAKE_REMOTE_HOME" exec sh -c "$cmd"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_REMOTE_HOME", remoteHome)
	return localHome, remoteHome
}

func writeSrc(t *testing.T, home, name string) string {
	t.Helper()
	p := filepath.Join(home, name)
	if err := os.WriteFile(p, []byte("APPVAL="+marker+"\nOTH=zzzz-1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, o Options) (string, string, error) {
	t.Helper()
	var so, se bytes.Buffer
	o.Stdout, o.Stderr = &so, &se
	if o.Machine == "" {
		o.Machine = "wbox2"
	}
	err := Run(o)
	return so.String(), se.String(), err
}

func TestCopyIs0600AndParentDir0700(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	dst := filepath.Join(rh, "proj", "sub", ".env")
	out, errs, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, errs)
	}
	if want := "copied " + dst + " mode 600\n"; out != want {
		t.Fatalf("stdout %q want %q", out, want)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dst mode %v err %v", fi, err)
	}
	di, _ := os.Stat(filepath.Dir(dst))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("parent dir mode %v", di.Mode().Perm())
	}
	if _, err := os.Stat(dst + ".sync-tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
	got, _ := os.ReadFile(dst)
	want, _ := os.ReadFile(src)
	if !bytes.Equal(got, want) {
		t.Fatal("content differs")
	}
}

func TestContentNeverPrinted(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	dst := filepath.Join(rh, ".env")
	var all string
	o, e, _ := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	all += o + e
	o, e, _ = run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh, DryRun: true})
	all += o + e
	t.Setenv("FAKE_SSH_FAIL", "1")
	o, e, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("want ErrFailed got %v", err)
	}
	all += o + e
	if all == "" || strings.Contains(all, marker) || strings.Contains(all, "zzzz-1234") {
		t.Fatalf("content leaked or no output: %q", all)
	}
}

func TestRedactsFileBytesFromSSHStderr(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	t.Setenv("FAKE_SSH_FAIL", "1")
	_, e, err := run(t, Options{Entries: []Entry{{src, filepath.Join(rh, ".env")}}, LocalHome: lh})
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("want ErrFailed got %v", err)
	}
	if strings.Contains(e, marker) {
		t.Fatalf("marker leaked: %q", e)
	}
	if !strings.Contains(e, "redacted") || !strings.Contains(e, "permission denied") {
		t.Fatalf("expected redaction note and passthrough of other lines: %q", e)
	}
}

func TestRedactFunc(t *testing.T) {
	got := string(Redact([]byte("a "+marker+" b\nok line"), []byte("APPVAL="+marker+"\n"+marker+"\n")))
	if strings.Contains(got, marker) || !strings.Contains(got, "ok line") {
		t.Fatalf("got %q", got)
	}
}

func TestModeCheckFailure(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	t.Setenv("FAKE_STAT", "644")
	_, e, err := run(t, Options{Entries: []Entry{{src, filepath.Join(rh, ".env")}}, LocalHome: lh})
	if !errors.Is(err, ErrFailed) || !strings.Contains(e, "mode check failed") {
		t.Fatalf("err=%v stderr=%q", err, e)
	}
}

func TestDryRunCopiesNothing(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	dst := filepath.Join(rh, ".env")
	out, _, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh, DryRun: true})
	if err != nil || out != "would copy "+src+" -> "+dst+"\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dry-run created the file")
	}
}

func TestPathGuard(t *testing.T) {
	lh, rh := fakeSSH(t)
	good := writeSrc(t, lh, ".env")
	link := filepath.Join(lh, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(lh, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	okDst := filepath.Join(rh, ".env")
	cases := []struct {
		name     string
		e        Entry
		wantPath string
	}{
		{"symlink", Entry{link, okDst}, link},
		{"directory", Entry{dir, okDst}, dir},
		{"outside-home", Entry{outside, okDst}, outside},
		{"dst-outside-remote-home", Entry{good, "/etc/x"}, "/etc/x"},
		{"dst-dotdot", Entry{good, rh + "/a/../../x"}, rh + "/a/../../x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := run(t, Options{Entries: []Entry{c.e}, LocalHome: lh})
			var ref *RefusedError
			if !errors.As(err, &ref) || !strings.Contains(ref.Msg, c.wantPath) {
				t.Fatalf("want refusal naming %s, got %v", c.wantPath, err)
			}
		})
	}
	if _, err := os.Stat("/etc/x"); err == nil {
		t.Fatal("/etc/x exists")
	}
	if _, _, err := run(t, Options{Entries: []Entry{{good, okDst}}, LocalHome: lh}); err != nil {
		t.Fatalf("regular file under home must copy: %v", err)
	}
}

func TestMachineGuard(t *testing.T) {
	for _, bad := range []string{"FactoryBox2", "my-FACTORY", "-oProxyCommand=x", "a b", ""} {
		var ref *RefusedError
		if err := ValidateMachine(bad); !errors.As(err, &ref) {
			t.Errorf("%q not refused", bad)
		}
	}
	for _, ok := range []string{"wbox", "wbox2", "wbox-2"} {
		if err := ValidateMachine(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	if err := ValidateMachine("FactoryBox2"); err == nil || err.Error() != `refused: machine name must not contain "factory"` {
		t.Errorf("message: %v", err)
	}
}

func TestLoadList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.json")
	os.WriteFile(p, []byte(`[{"src":"/a","dst":"/b"}]`), 0o600)
	es, err := LoadList(p)
	if err != nil || len(es) != 1 || es[0].Dst != "/b" {
		t.Fatalf("%v %v", es, err)
	}
	os.WriteFile(p, []byte(`{"nope":1}`), 0o600)
	if _, err := LoadList(p); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestRemoteSymlinkedParentRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rh, "x")); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, Options{Entries: []Entry{{src, filepath.Join(rh, "x", "new", ".env")}}, LocalHome: lh})
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("want ErrFailed got %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("wrote outside remote home: %v", ents)
	}
}

func TestQuotedDstAndStaleWorldReadableTemp(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	dst := filepath.Join(rh, "a b", "it's.env")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := dst + ".sync-tmp"
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, e, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh}); err != nil {
		t.Fatalf("%v %s", err, e)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dst: %v %v", fi, err)
	}
}

// swapAfterCheck replaces src with a symlink to (or a different file holding) outside content between check and read.
func swapAfterCheck(t *testing.T, swap func()) {
	t.Helper()
	prev := afterCheck
	afterCheck = swap
	t.Cleanup(func() { afterCheck = prev })
}

func wantRefusal(t *testing.T, err error, path string) {
	t.Helper()
	var ref *RefusedError
	if !errors.As(err, &ref) || !strings.Contains(ref.Msg, path) {
		t.Fatalf("want refusal naming %s, got %v", path, err)
	}
}

func TestSrcSwappedForSymlinkAfterCheckRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("OUTSIDE-ONLY-77aa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	swapAfterCheck(t, func() {
		if err := os.Remove(src); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, src); err != nil {
			t.Fatal(err)
		}
	})
	dst := filepath.Join(rh, ".env")
	_, _, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	wantRefusal(t, err, src)
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("swapped symlink target was copied")
	}
}

func TestSrcReplacedByOtherFileAfterCheckRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	other := filepath.Join(lh, "other")
	if err := os.WriteFile(other, []byte("OTHER=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	swapAfterCheck(t, func() {
		if err := os.Rename(other, src); err != nil {
			t.Fatal(err)
		}
	})
	dst := filepath.Join(rh, ".env")
	_, _, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	wantRefusal(t, err, src)
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("replaced file was copied")
	}
}

func TestOversizeFileRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	big := filepath.Join(lh, "big")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SSH_ARGS", filepath.Join(t.TempDir(), "args"))
	_, _, err := run(t, Options{Entries: []Entry{{big, filepath.Join(rh, "big")}}, LocalHome: lh})
	wantRefusal(t, err, big)
	if _, err := os.Stat(os.Getenv("FAKE_SSH_ARGS")); err == nil {
		t.Fatal("ssh was called for an oversize file")
	}
	// exactly at the cap is accepted
	if err := os.Truncate(big, MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	if _, e, err := run(t, Options{Entries: []Entry{{big, filepath.Join(rh, "big")}}, LocalHome: lh}); err != nil {
		t.Fatalf("file at the cap refused: %v %s", err, e)
	}
}

func TestOversizeTotalRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	var es []Entry
	for i := 0; i*MaxFileBytes <= MaxTotalBytes; i++ {
		p := filepath.Join(lh, fmt.Sprintf("f%02d", i))
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, MaxFileBytes); err != nil {
			t.Fatal(err)
		}
		es = append(es, Entry{p, filepath.Join(rh, fmt.Sprintf("f%02d", i))})
	}
	_, _, err := run(t, Options{Entries: es, LocalHome: lh, DryRun: true})
	wantRefusal(t, err, "total size")
	// exactly at the total cap is accepted
	if _, e, err := run(t, Options{Entries: es[:MaxTotalBytes/MaxFileBytes], LocalHome: lh, DryRun: true}); err != nil {
		t.Fatalf("list at the total cap refused: %v %s", err, e)
	}
}

func TestOversizeListRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.json")
	body := append([]byte(`[{"src":"/a","dst":"/b"}]`), bytes.Repeat([]byte(" "), MaxListBytes)...) // valid JSON
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadList(p)
	wantRefusal(t, err, p)
	if err := os.WriteFile(p, body[:MaxListBytes], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadList(p); err != nil {
		t.Fatalf("list at the cap refused: %v", err)
	}
}

func TestSrcGrownAfterCheckRefused(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	swapAfterCheck(t, func() {
		if err := os.Truncate(src, MaxFileBytes+1); err != nil {
			t.Fatal(err)
		}
	})
	dst := filepath.Join(rh, ".env")
	_, _, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	wantRefusal(t, err, "larger than")
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("grown file was copied")
	}
}

func TestWholeRunDeadline(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	t.Setenv("FAKE_SSH_HANG", "1")
	start := time.Now()
	_, e, err := run(t, Options{Entries: []Entry{{src, filepath.Join(rh, ".env")}}, LocalHome: lh,
		Timeout: time.Minute, RunTimeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrFailed) || !strings.Contains(e, "whole-run deadline") {
		t.Fatalf("err=%v stderr=%q", err, e)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("hung for %s", d)
	}
}

func TestSSHTimeoutFailsAndPassesConnectTimeout(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_SSH_ARGS", args)
	t.Setenv("FAKE_SSH_HANG", "1")
	start := time.Now()
	_, e, err := run(t, Options{Entries: []Entry{{src, filepath.Join(rh, ".env")}}, LocalHome: lh, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrFailed) || !strings.Contains(e, "timed out") {
		t.Fatalf("err=%v stderr=%q", err, e)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("hung for %s", d)
	}
	a, _ := os.ReadFile(args)
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=10", "ServerAliveInterval=5"} {
		if !strings.Contains(string(a), want) {
			t.Fatalf("ssh args %q missing %s", a, want)
		}
	}
}

func TestDstExistingDirectoryFails(t *testing.T) {
	lh, rh := fakeSSH(t)
	src := writeSrc(t, lh, ".env")
	dst := filepath.Join(rh, "adir")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	_, e, err := run(t, Options{Entries: []Entry{{src, dst}}, LocalHome: lh})
	if !errors.Is(err, ErrFailed) || !strings.Contains(e, "dst is a directory") {
		t.Fatalf("err=%v stderr=%q", err, e)
	}
	if ents, _ := os.ReadDir(dst); len(ents) != 0 {
		t.Fatalf("wrote into the directory: %v", ents)
	}
}
