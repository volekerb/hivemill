package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCleanRepo(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--root", filepath.Join("..", "..", "..")}, &out, &errb)
	if code != 0 || !strings.HasPrefix(out.String(), "kit-check: ok (") {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
}

func TestRunFindingsExitOne(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.HasPrefix(out.String(), "kit-check: ") || strings.Contains(out.String(), "ok (") {
		t.Fatalf("out %q", out.String())
	}
}

func TestRunUsageAndVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--bogus"}, &out, &errb); code != 2 {
		t.Fatalf("bad flag: code %d", code)
	}
	if code := run([]string{"extra"}, &out, &errb); code != 2 {
		t.Fatalf("extra arg: code %d", code)
	}
	out.Reset()
	if code := run([]string{"--version"}, &out, &errb); code != 0 || out.String() != "kit-check dev\n" {
		t.Fatalf("version: code %d out %q", code, out.String())
	}
}
