package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// T-1001 (spec v2): the Go guard does not read archives. A no-push extraction that passes the grammar and target
// checks is delegated to the Python hook (the reference: tarfile and zipfile), so crafted archives must give the
// same exit and stderr through the Go binary (with --py-hook) as through the Python hook itself.

func archTar(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, n := range names {
		if err := w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func archZip(t *testing.T, name string, extra []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateRaw(&zip.FileHeader{Name: name, Extra: extra, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	_ = f
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// unicodePathExtra is the 0x7075 Info-ZIP Unicode Path extra: version 1, CRC32 of the stored name, UTF-8 name.
func unicodePathExtra(stored, unicode string) []byte {
	crc := crc32.ChecksumIEEE([]byte(stored))
	body := append([]byte{1, byte(crc), byte(crc >> 8), byte(crc >> 16), byte(crc >> 24)}, unicode...)
	return append([]byte{0x75, 0x70, byte(len(body)), byte(len(body) >> 8)}, body...)
}

func pyBytes(t *testing.T, code string, in []byte) []byte {
	t.Helper()
	cmd := exec.Command("python3", "-I", "-c", code)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3 helper: %v", err)
	}
	return out
}

func TestArchiveMemberListMatchesPythonHook(t *testing.T) {
	hook := absHook(t)
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(scratch, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := archTar(t, "f", "d/g")
	bad := archTar(t, "x/.git/config")
	cut := func(b []byte, n int) []byte { return b[:n] }
	bz := func(level string, b []byte) []byte {
		return pyBytes(t, "import bz2,sys;sys.stdout.buffer.write(bz2.compress(sys.stdin.buffer.read(),"+level+"))", b)
	}
	files := map[string][]byte{
		"ok.tar": ok, "bad.tar": bad, "ok.tgz": nil, "cut513.tar": cut(archTar(t, "f", "g"), 513), "cut700.tar": cut(append(append([]byte{}, ok...), make([]byte, 400)...), 700),
		"bz1.tar.bz2": bz("1", ok), "bz9.tar.bz2": bz("9", ok), "bzbad1.tar.bz2": bz("1", bad),
		"lead0.tar": append(make([]byte, 512), ok...), "trail.tar": append(append([]byte{}, ok...), 'x', 'y', 'z'),
		"nul.zip":   archZip(t, "x/.git\x00Q", nil),
		"u7075.zip": archZip(t, "benign.txt", unicodePathExtra("benign.txt", ".git/config")),
		"clean.zip": archZip(t, "f", nil), "empty.tar": nil, "junk.tar": []byte("not an archive at all"),
	}
	files["ok.tgz"] = pyBytes(t, "import gzip,sys;sys.stdout.buffer.write(gzip.compress(sys.stdin.buffer.read()))", ok)
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(scratch, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmds := []string{
		"unzip nul.zip -d " + out, "unzip u7075.zip -d " + out, "unzip clean.zip -d " + out, "unzip gone.zip -d " + out,
		"tar -xf cut513.tar -C " + out, "tar -xf cut700.tar -C " + out, "tar -xjf bz1.tar.bz2 -C " + out, "tar -xjf bz9.tar.bz2 -C " + out,
		"tar -xjf bzbad1.tar.bz2 -C " + out, "tar -xf lead0.tar -C " + out, "tar -xf trail.tar -C " + out, "tar -xf ok.tar -C " + out,
		"tar -xzf ok.tgz -C " + out, "tar -xf bad.tar -C " + out, "tar -xf empty.tar -C " + out, "tar -xf junk.tar -C " + out,
		"tar -xf gone.tar -C " + out, "noglob cat ok.tar; tar -xf n.tar -C " + out, "cat ok.tar > n.tar; tar -xf n.tar -C " + out,
		"bash -c 'tar -xf bad.tar -C " + out + "'", "bash -c 'tar -xf ok.tar -C " + out + "'",
	}
	env := append(withTerms(t), "WORKER_GUARD_SCRATCH="+scratch)
	for _, role := range []string{"qa", "pm"} {
		for _, c := range cmds {
			payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c) + `},"cwd":` + jsonString(scratch) + `}`)
			code, stderr := call(t, payload, env, "--role", role, "--py-hook", hook)
			pcode, pstderr := callPython(t, hook, payload, env, "--role", role)
			if code != pcode || stderr != pstderr {
				t.Errorf("%s %q: go exit %d %q, python exit %d %q", role, c, code, stderr, pcode, pstderr)
			}
			if strings.Contains(stderr, "failing closed") {
				t.Errorf("%s %q: hook error instead of a decision: %q", role, c, stderr)
			}
		}
	}
	// the crafted ones must really be refused by Python (the test would be vacuous otherwise)
	for _, c := range []string{"unzip nul.zip -d " + out, "unzip u7075.zip -d " + out, "tar -xf cut513.tar -C " + out} {
		payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c) + `},"cwd":` + jsonString(scratch) + `}`)
		if code, _ := callPython(t, hook, payload, env, "--role", "qa"); code != 2 {
			t.Errorf("python exit %d for %q, want 2", code, c)
		}
	}
}

// RunPython failing closed: no --py-hook, or a hook that crashes, is exit 2 for a delegated extraction, never 0.
func TestArchiveDelegationFailsClosed(t *testing.T) {
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "a.tar"), archTar(t, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(withTerms(t), "WORKER_GUARD_SCRATCH="+scratch)
	payload := []byte(`{"tool_name":"Bash","tool_input":{"command":"tar -xf a.tar -C ` + scratch + `"},"cwd":` + jsonString(scratch) + `}`)
	if code, stderr := call(t, payload, env, "--role", "qa"); code != 2 || !strings.Contains(stderr, "failing closed") {
		t.Errorf("no --py-hook: exit %d %q, want 2 failing closed", code, stderr)
	}
	if code, stderr := call(t, payload, env, "--role", "qa", "--py-hook", failingHookLayout(t)); code != 2 || !strings.Contains(stderr, "failing closed") {
		t.Errorf("crashing hook: exit %d %q, want 2 failing closed", code, stderr)
	}
	if code, stderr := call(t, payload, env, "--role", "qa", "--py-hook", filepath.Join(scratch, "missing.py")); code != 2 {
		t.Errorf("missing hook: exit %d %q, want 2", code, stderr)
	}
	// python3 missing from PATH
	env2 := append(append([]string{}, env...), "PATH="+t.TempDir())
	if code, stderr := call(t, payload, env2, "--role", "qa", "--py-hook", absHook(t)); code != 2 {
		t.Errorf("no python3: exit %d %q, want 2", code, stderr)
	}
}
