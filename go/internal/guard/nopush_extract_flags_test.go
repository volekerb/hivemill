package guard

import (
	"os"
	"testing"
)

// T-0985 (tests/test_worker_guard_nopush_extract_flags.py, Python side T-0808): no-push roles may not bypass the extraction
// bound with python -W/-X values, python long options, `ar @file`, `python -c` extraction code or an inline interpreter
// writer before an extraction. Benign neighbours stay allowed. The cases are the Python module's.

func TestNoPushExtractFlagsRefused(t *testing.T) {
	a := newArchiveFix(t)
	s, z, git, clean := a.scratch, a.scratch+"/a.zip", a.repo+"/.git", a.scratch+"/clean"
	a.expect(t, noPushRolesList, s, 2,
		// F1: -W/-X values before -m
		"python3 -Wonce -m zipfile -e "+z+" "+git, "python3 -Xpycache_prefix=x -m zipfile -e "+z+" "+git,
		"python3 -W once -m zipfile -e "+z+" "+git, "python3 -X dev -m zipfile -e "+z+" "+git,
		// F2: long options before -m
		"python3 --check-hash-based-pycs always -m zipfile -e "+z+" "+git,
		"python3 --check-hash-based-pycs=always -m zipfile -e "+z+" "+git,
		"python3 --some-unknown-opt -m zipfile -e "+z+" "+git,
		// F3: ar response file
		"ar @optsfile a.a", "ar x @optsfile", "ar @opts", "ar --target @opts a.a", "ar --plugin @opts a.a", "ar --output @opts a.a",
		// F4: python -c extraction code
		`python3 -c "import zipfile; zipfile.ZipFile('`+z+`').extractall('`+git+`')"`,
		`python3 -c 'import tarfile; tarfile.open("a.tar").extract("x")'`,
		`python3 -c 'import tarfile; tarfile.open("a.tar").extract ("x")'`,
		`python3 -c 'import shutil; shutil.unpack_archive("a.zip", "r/.git")'`,
		`python -c 'import zipfile as z; z.ZipFile("a.zip").extractall("o")'`,
		// F5: an inline interpreter program counts as a filesystem change before an extraction
		`python3 -c 'import os; os.symlink("`+git+`", "`+clean+`/l")' && unzip `+z+" -d "+clean,
		`python3 -c 'open("x", "w")'; unzip `+z+" -d "+clean,
		`python3 -c'open("x", "w")'; unzip `+z+" -d "+clean,
		`python3 -Bc 'open("x", "w")'; unzip `+z+" -d "+clean,
		"echo x | python3; unzip "+z+" -d "+clean,
		"python3 - <<'E'\nopen('x', 'w')\nE\nunzip "+z+" -d "+clean,
		"python3 <<'E'\nopen('x', 'w')\nE\nunzip "+z+" -d "+clean,
		// B1: explicit or implicit stdin program, non-python
		"node - <<'E'\n1\nE\nunzip "+z+" -d "+clean, "ruby - <<'E'\n1\nE\nunzip "+z+" -d "+clean,
		"lua - <<'E'\n1\nE\nunzip "+z+" -d "+clean, "php - <<'E'\n1\nE\nunzip "+z+" -d "+clean,
		"node <<'E'\n1\nE\nunzip "+z+" -d "+clean, "echo 1 | ruby; unzip "+z+" -d "+clean)
}

func TestNoPushExtractFlagsBenignAllowed(t *testing.T) {
	a := newArchiveFix(t)
	s, z, clean := a.scratch, a.scratch+"/a.zip", a.scratch+"/clean"
	if err := os.MkdirAll(clean, 0o755); err != nil {
		t.Fatal(err)
	}
	cmds := []string{
		"python3 -Wonce -m json.tool", "python3 --check-hash-based-pycs=always -m json.tool", "python3 -m zipfile -l " + z,
		"python3 -Wonce -m zipfile -e " + z + " " + clean, "python3 --check-hash-based-pycs always -m zipfile -l " + z,
		"ar t a.a", "python3 -c 'print(1)'", "unzip " + z + " -d " + clean,
	}
	// T-0808 F5 neighbours: no inline writer, so the extraction after `;` stays allowed
	for _, c := range []string{"python3 -Wonce -m json.tool f", "python3 -Wignore -m json.tool f", "python3 -Xdev -m json.tool f",
		"node -type x", "python3 --version", "python3 -V", "python3 -h", "python3 --help",
		// non-python option forms are not tightened (T-0953)
		"node x.js -c", "node -r x.js", "ruby -c x.rb", "ruby -E utf-8 x.rb", "node -print x", "node -e code", "php -r code"} {
		cmds = append(cmds, c+" ; unzip "+z+" -d "+clean)
	}
	a.expect(t, noPushRolesList, s, 0, cmds...)
}

func TestPyInlineCode(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"-c", "x"}, true}, {[]string{"-Bc", "x"}, true}, {[]string{"-c'x'"}, true}, {nil, true}, {[]string{"-"}, true},
		{[]string{"-W", "once", "-c", "x"}, true}, {[]string{"--check-hash-based-pycs", "always", "-"}, true},
		{[]string{"-m", "json.tool"}, false}, {[]string{"-Wonce", "-m", "json.tool"}, false}, {[]string{"x.py"}, false},
		{[]string{"--version"}, false}, {[]string{"-V"}, false}, {[]string{"-h"}, false}, {[]string{"--help-all"}, false},
	} {
		if got := pyInlineCode(c.args); got != c.want {
			t.Errorf("pyInlineCode(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
