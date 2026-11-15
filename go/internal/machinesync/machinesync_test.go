package machinesync

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		{"dst-dotdot", Entry{good, rh + "/a/../../x"}, "x"},
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
