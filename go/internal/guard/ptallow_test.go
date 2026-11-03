package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// T-0887 review round 2: the allowlist reads like ci/scrub_scan.py's Allowlist (Unicode \w \d \s, strict UTF-8).

const pyAllow = `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("ss", sys.argv[1] + "/ci/scrub_scan.py")
ss = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ss)
req = json.load(sys.stdin)
out = []
try:
    al = ss.Allowlist.load(req["file"])
    out = [al.allows([c[0]], c[1], c[2]) for c in req["cases"]]
except ss.UsageError as e:
    out = "usage: " + str(e)
except Exception as e:
    out = "error: %s: %s" % (type(e).__name__, e)
json.dump(out, sys.stdout)
`

func pyAllowVerdicts(t *testing.T, file string, cases [][3]string) any {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"file": file, "cases": cases})
	cmd := exec.Command("python3", "-I", "-c", pyAllow, root)
	cmd.Stdin = bytes.NewReader(req)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python allowlist: %v\n%s", err, stderr.String())
	}
	var res any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("python allowlist output: %v\n%s", err, out)
	}
	return res
}

func TestAllowlistRegexesAreUnicodeLikePython(t *testing.T) {
	lines := []string{
		`caf.txt:email:^[\w./-]+$  # w in a class`,
		`d.txt:email:^\w+$  # w`,
		`n.txt:email:^\d+$  # d`,
		`s.txt:email:^a\sb$  # s`,
		`W.txt:email:^\W+$  # W`,
		`D.txt:email:^\D+$  # D`,
		`S.txt:email:^a\Sb$  # S`,
		`cw.txt:email:^[\W]+$  # W in a class`,
		`cd.txt:email:^[\D]+$  # D in a class`,
		`cs.txt:email:^[\S]+$  # S in a class`,
		`neg.txt:email:^[^\w]+$  # negated class`,
		`mix.txt:email:^[\w\s]+!$  # w and s together`,
		`lit.txt:email:^[]a]+$  # leading bracket`,
		`legacy.txt:^[\w]+$  # legacy form`,
	}
	file := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cases [][3]string
	for _, f := range []string{"caf.txt", "d.txt", "n.txt", "s.txt", "W.txt", "D.txt", "S.txt", "cw.txt", "cd.txt", "cs.txt", "neg.txt", "mix.txt", "lit.txt", "legacy.txt"} {
		for _, v := range []string{"café.txt", "abc", "straße", "٣٤", "12", "²", "a b", "a b", "a b", "a　b", "a\x1cb", "a\x0bb", "a\x85b",
			"é", "ǅ", "ⅷ", "日本", "_", "-", "!", "a-b", "é !", "]a]", "à", "éⅠ"} {
			cases = append(cases, [3]string{f, v, "email"})
		}
	}
	want, ok := pyAllowVerdicts(t, file, cases).([]any)
	if !ok {
		t.Fatalf("python could not load the allowlist: %v", pyAllowVerdicts(t, file, cases))
	}
	allow, err := loadAllowlist(file)
	if err != nil {
		t.Fatal(err)
	}
	allowed := 0
	for i, c := range cases {
		got := allow.allows(c[0], c[1], c[2])
		if got {
			allowed++
		}
		if got != want[i] {
			t.Errorf("%s %q: go %v, python %v", c[0], c[1], got, want[i])
		}
	}
	if allowed == 0 || allowed == len(cases) {
		t.Errorf("%d of %d cases allowed: the corpus does not separate anything", allowed, len(cases))
	}
}

func TestAllowlistInvalidUTF8FailsLikePython(t *testing.T) {
	for name, raw := range map[string][]byte{
		"start byte":    []byte("a.txt:email:x  # r\xff\n"),
		"continuation":  []byte("# c\n\xe2\x28\xa1\n"),
		"truncated":     []byte("a.txt:email:x  # r \xe2\x82"),
		"surrogate":     []byte("\xed\xa0\x80"),
		"overlong lead": []byte("\xc0\xaf"),
		"four byte cut": []byte("ok\n\xf0\x90\x80"),
		"too large":     []byte("\xf4\x90\x80\x80"),
	} {
		file := filepath.Join(t.TempDir(), "allow.txt")
		if err := os.WriteFile(file, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadAllowlist(file)
		if err == nil {
			t.Errorf("%s: invalid UTF-8 loaded", name)
			continue
		}
		if _, usage := err.(*usageError); usage {
			t.Errorf("%s: %v is a usage error; Python's is a crash that fails closed", name, err)
		}
		want := pyAllowVerdicts(t, file, [][3]string{})
		if want != "error: "+err.Error() {
			t.Errorf("%s: go %q, python %v", name, err.Error(), want)
		}
	}
}

func TestAllowlistRegexNeedingBoundaryFailsClosed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(file, []byte("a.txt:email:\\bx  # boundary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllowlist(file); err == nil || !strings.Contains(err.Error(), "bad regex") {
		t.Errorf("a \\b regex must be refused with a bad regex message, got %v", err)
	}
}
