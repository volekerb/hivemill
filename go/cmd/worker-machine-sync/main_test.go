package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary act as the real worker-machine-sync binary (run through os/exec).
func TestMain(m *testing.M) {
	if os.Getenv("WMS_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func setup(t *testing.T) (home, remote, list string) {
	t.Helper()
	root := t.TempDir()
	home, remote = filepath.Join(root, "home"), filepath.Join(root, "remote")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{home, remote, bin} {
		os.MkdirAll(d, 0o755)
	}
	script := "#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nHOME=\"$FAKE_REMOTE_HOME\" exec sh -c \"$*\"\n"
	os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755)
	src := filepath.Join(home, ".env")
	os.WriteFile(src, []byte("APPVAL=FIXTURE-VALUE-9f2c\n"), 0o644)
	list = filepath.Join(root, "l.json")
	os.WriteFile(list, []byte(`[{"src":"`+src+`","dst":"`+filepath.Join(remote, ".env")+`"}]`), 0o600)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_REMOTE_HOME", remote)
	t.Setenv("HOME", home)
	return
}

func runBin(t *testing.T, args ...string) (code int, out, errs string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "WMS_AS_MAIN=1")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, so.String(), se.String()
}

func TestBinaryMachineNameGuard(t *testing.T) {
	_, _, list := setup(t)
	code, _, errs := runBin(t, "--machine", "FactoryBox2", "--list", list, "--dry-run")
	if code != 2 || !strings.Contains(errs, `refused: machine name must not contain "factory"`) {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	code, out, errs := runBin(t, "--machine", "wbox2", "--list", list, "--dry-run")
	if code != 0 || !strings.HasPrefix(out, "would copy ") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
}

func TestBinaryCopyAndUsage(t *testing.T) {
	_, remote, list := setup(t)
	code, out, errs := runBin(t, "--machine", "wbox2", "--list", list)
	if code != 0 || out != "copied "+filepath.Join(remote, ".env")+" mode 600\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
	if strings.Contains(out+errs, "FIXTURE-VALUE-9f2c") {
		t.Fatal("content printed")
	}
	if fi, err := os.Stat(filepath.Join(remote, ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("remote file: %v %v", fi, err)
	}
	if code, _, _ := runBin(t, "--machine", "wbox2"); code != 2 {
		t.Fatalf("missing --list must exit 2, got %d", code)
	}
}
