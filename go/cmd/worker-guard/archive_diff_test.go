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

// T-1001/T-2112: the Go guard reads the member list of a no-push extraction itself (archive_list.go, a port of the
// Python hook's tarfile/zipfile reading), so crafted archives must give the same exit and stderr through the Go
// binary (no python involved) as through the Python hook, the reference.

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
	if _, err := w.CreateRaw(&zip.FileHeader{Name: name, Extra: extra, Method: zip.Store}); err != nil {
		t.Fatal(err)
	}
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
	mk := func(format string, names ...string) []byte { // tarfile in the given format, so pax and GNU long headers appear
		code := "import io,sys,tarfile;b=io.BytesIO();t=tarfile.open(fileobj=b,mode='w',format=tarfile." + format + ")\n" +
			"for n in sys.stdin.read().split('\\n'):\n t.addfile(tarfile.TarInfo(n))\nt.close();sys.stdout.buffer.write(b.getvalue())"
		return pyBytes(t, code, []byte(strings.Join(names, "\n")))
	}
	long := strings.Repeat("d/", 60)
	files["pax.tar"] = mk("PAX_FORMAT", "f", long+".git/config")
	files["paxok.tar"] = mk("PAX_FORMAT", "f", long+"g", "\u00e9t\u00e9")
	files["gnu.tar"] = mk("GNU_FORMAT", "f", long+".git/config")
	files["gnuok.tar"] = mk("GNU_FORMAT", long+"g")
	files["cat.tar"] = append(append(append([]byte{}, ok...), make([]byte, 2048)...), bad...)
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
		"tar -xf pax.tar -C " + out, "tar -xf paxok.tar -C " + out, "tar -xf gnu.tar -C " + out, "tar -xf gnuok.tar -C " + out,
		"tar -xf cat.tar -C " + out, "bash -c 'tar -xf bad.tar -C " + out + "'", "bash -c 'tar -xf ok.tar -C " + out + "'",
	}
	env := append(withTerms(t), "WORKER_GUARD_SCRATCH="+scratch, "FACTORY_HOME="+t.TempDir())
	for _, role := range []string{"qa", "pm"} {
		for _, c := range cmds {
			payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c) + `},"cwd":` + jsonString(scratch) + `}`)
			code, stderr := call(t, payload, env, "--role", role)
			pcode, pstderr := callPython(t, hook, payload, env, "--role", role)
			if code != pcode || stderr != pstderr {
				t.Errorf("%s %q: go exit %d %q, python exit %d %q", role, c, code, stderr, pcode, pstderr)
			}
			if strings.Contains(stderr, "failing closed") {
				t.Errorf("%s %q: hook error instead of a decision: %q", role, c, stderr)
			}
		}
	}
	// the equality above would also hold if both sides refused everything: pin the reference's own answers
	for c, want := range map[string]int{
		"unzip nul.zip -d " + out: 2, "unzip u7075.zip -d " + out: 2, "tar -xf cut513.tar -C " + out: 2, "tar -xf bad.tar -C " + out: 2,
		"tar -xjf bzbad1.tar.bz2 -C " + out: 2, "tar -xf ok.tar -C " + out: 0, "tar -xzf ok.tgz -C " + out: 0, "unzip clean.zip -d " + out: 0,
		"unzip clean -d " + out: 0, "tar -xf pax.tar -C " + out: 2, "tar -xf gnu.tar -C " + out: 2, "tar -xf cat.tar -C " + out: 2,
		"tar -xf paxok.tar -C " + out: 0, "tar -xf gnuok.tar -C " + out: 0,
	} {
		payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c) + `},"cwd":` + jsonString(scratch) + `}`)
		if code, stderr := callPython(t, hook, payload, env, "--role", "qa"); code != want {
			t.Errorf("python exit %d for %q, want %d (%s)", code, c, want, stderr)
		}
	}
}

// T-2112: the member list never runs python: a clean extraction is allowed in Go alone, whatever --py-hook names
// (accepted and ignored) and with no python3 on PATH.
func TestArchiveDelegationFailsClosed(t *testing.T) {
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "a.tar"), archTar(t, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "b.tar"), archTar(t, "x/.git/config"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(withTerms(t), "WORKER_GUARD_SCRATCH="+scratch)
	pay := func(arch string) []byte {
		return []byte(`{"tool_name":"Bash","tool_input":{"command":"tar -xf ` + arch + ` -C ` + scratch + `"},"cwd":` + jsonString(scratch) + `}`)
	}
	noPy := append(append([]string{}, env...), "PATH="+t.TempDir())
	for _, c := range []struct {
		what string
		env  []string
		args []string
	}{
		{"no --py-hook", env, nil},
		{"crashing hook", env, []string{"--py-hook", failingHookLayout(t)}},
		{"missing hook", env, []string{"--py-hook", filepath.Join(scratch, "missing.py")}},
		{"no python3", noPy, []string{"--py-hook", absHook(t)}},
	} {
		args := append([]string{"--role", "qa"}, c.args...)
		if code, stderr := call(t, pay("a.tar"), c.env, args...); code != 0 {
			t.Errorf("%s: clean a.tar: exit %d %q, want 0", c.what, code, stderr)
		}
		if code, stderr := call(t, pay("b.tar"), c.env, args...); code != 2 || !strings.Contains(stderr, "under a .git path") {
			t.Errorf("%s: b.tar with .git: exit %d %q, want 2 with the member block", c.what, code, stderr)
		}
	}
}
