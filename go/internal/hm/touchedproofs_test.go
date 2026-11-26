package hm

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func touchedWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touchedGit(t *testing.T, root string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// touchedRepo is a repo with packages a and b on main, and a branch that changes the named files.
func touchedRepo(t *testing.T, changed ...string) string {
	t.Helper()
	root := t.TempDir()
	touchedGit(t, root, "init", "-q", "-b", "main")
	touchedWrite(t, root, "a/package.json", `{"scripts":{"test":"tsx test.ts"}}`)
	touchedWrite(t, root, "b/package.json", `{"devDependencies":{"vitest":"1"}}`)
	touchedWrite(t, root, "README.md", "x\n")
	touchedGit(t, root, "add", "-A")
	touchedGit(t, root, "commit", "-qm", "base")
	touchedGit(t, root, "checkout", "-qb", "work")
	for _, c := range changed {
		touchedWrite(t, root, c, "changed\n")
	}
	touchedGit(t, root, "add", "-A")
	touchedGit(t, root, "commit", "-qm", "work", "--allow-empty")
	return root
}

// touchedRun runs the proofs with a fake Exec that fails the packages in red; it returns the exit code, the output
// and the directories Exec ran in.
func touchedRun(root string, red ...string) (int, string, []string) {
	var out bytes.Buffer
	var ran []string
	code := TouchedRun{Root: root, Base: "main", Out: &out, Exec: func(dir string, _ []string) (int, error) {
		ran = append(ran, filepath.Base(dir))
		if slices.Contains(red, filepath.Base(dir)) {
			return 1, nil
		}
		return 0, nil
	}}.Run()
	return code, out.String(), ran
}

func TestTouchedPackagesNearestPackageJSON(t *testing.T) {
	root := t.TempDir()
	touchedWrite(t, root, "a/package.json", "{}")
	touchedWrite(t, root, "a/nested/package.json", "{}")
	got := TouchedPackages(root, []string{"a/nested/src/x.ts", "a/src/deep/gone.ts", "a/y.ts", "a/package.json", "README.md", "other/z.ts"})
	if want := []string{"a", "a/nested"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	touchedWrite(t, root, "package.json", "{}")
	if got := TouchedPackages(root, []string{"README.md"}); !slices.Equal(got, []string{"."}) {
		t.Fatalf("root package: got %v", got)
	}
}

func TestPackageTestArgvScriptVitestNone(t *testing.T) {
	for _, c := range []struct {
		body string
		want string
	}{
		{`{"scripts":{"test":"vitest run"},"devDependencies":{"vitest":"1"}}`, "npm run test --prefix DIR"},
		{`{"devDependencies":{"vitest":"1"}}`, "npx vitest run --root DIR"},
		{`{"dependencies":{"vitest":"1"}}`, "npx vitest run --root DIR"},
		{`{"scripts":{"build":"tsc"}}`, ""},
	} {
		dir := t.TempDir()
		touchedWrite(t, dir, "package.json", c.body)
		argv, ok := PackageTestArgv(dir)
		if got := strings.ReplaceAll(strings.Join(argv, " "), dir, "DIR"); got != c.want || ok != (c.want != "") {
			t.Errorf("%s: got %q %v, want %q", c.body, got, ok, c.want)
		}
	}
}

func TestTouchedRunFailsOnRedPackage(t *testing.T) {
	root := touchedRepo(t, "a/x.ts", "b/y.ts")
	code, out, ran := touchedRun(root, "a")
	if code != 1 || !slices.Equal(ran, []string{"a", "b"}) {
		t.Fatalf("code %d ran %v: %s", code, ran, out)
	}
	if !strings.Contains(out, "FAIL npm run test --prefix ") || !strings.Contains(out, "PASS npx vitest run --root ") {
		t.Fatalf("output: %s", out)
	}
	// A package.json the branch broke fails; it is not skipped as having no tests.
	root = touchedRepo(t, "a/package.json")
	if code, out, ran := touchedRun(root); code != 1 || len(ran) != 0 || !strings.HasPrefix(out, "FAIL a: package.json: ") {
		t.Fatalf("broken package.json: code %d ran %v: %s", code, ran, out)
	}
	var buf bytes.Buffer
	if code := (TouchedRun{Root: root, Base: "no-such-ref", Out: &buf}).Run(); code != 2 {
		t.Fatalf("bad base: code %d: %s", code, buf.String())
	}
}

func TestTouchedRunAllGreen(t *testing.T) {
	root := touchedRepo(t, "a/x.ts", "b/y.ts")
	code, out, _ := touchedRun(root)
	want := "PASS npm run test --prefix " + filepath.Join(root, "a") + "\n" +
		"PASS npx vitest run --root " + filepath.Join(root, "b") + "\n"
	if code != 0 || out != want {
		t.Fatalf("code %d, out %q, want %q", code, out, want)
	}
}

// touchedXcode runs the iOS proof of root with a green fake Exec; it returns the exit code, the output and each call
// as its directory followed by its argv.
func touchedXcode(root, goos, scheme string) (int, string, [][]string) {
	var out bytes.Buffer
	var calls [][]string
	code := TouchedRun{Root: root, Base: "main", Out: &out, GOOS: goos, IOSDir: "ios", Workspace: "App.xcworkspace",
		Scheme: scheme, Exec: func(dir string, argv []string) (int, error) {
			calls = append(calls, append([]string{dir}, argv...))
			return 0, nil
		}}.Run()
	return code, out.String(), calls
}

func TestTouchedXcodeRunsOnDarwin(t *testing.T) {
	root := touchedRepo(t, "ios/App/Main.swift")
	code, out, calls := touchedXcode(root, "darwin", "App")
	dir := filepath.Join(root, "ios")
	want := []string{dir, "xcodebuild", "-workspace", filepath.Join(dir, "App.xcworkspace"), "-scheme", "App",
		"-destination", "generic/platform=iOS Simulator", "-configuration", "Debug", "build", "CODE_SIGNING_ALLOWED=NO"}
	if code != 0 || len(calls) != 1 || !slices.Equal(calls[0], want) || !strings.Contains(out, "PASS xcodebuild\n") {
		t.Fatalf("code %d calls %q: %s", code, calls, out)
	}
	// A project instead of a workspace, and a red build.
	var buf bytes.Buffer
	var argv []string
	code = TouchedRun{Root: root, Base: "main", Out: &buf, GOOS: "darwin", IOSDir: "ios/", Project: "App.xcodeproj",
		Scheme: "App", Exec: func(_ string, a []string) (int, error) { argv = a; return 65, nil }}.Run()
	if code != 1 || !slices.Equal(argv[1:3], []string{"-project", filepath.Join(dir, "App.xcodeproj")}) ||
		!strings.Contains(buf.String(), "FAIL xcodebuild\n") {
		t.Fatalf("project: code %d argv %q: %s", code, argv, buf.String())
	}
}

func TestTouchedXcodeFailsOffDarwin(t *testing.T) {
	code, out, calls := touchedXcode(touchedRepo(t, "ios/App/Main.swift"), "linux", "App")
	if code != 1 || len(calls) != 0 || !strings.Contains(out, "FAIL xcodebuild: needs a darwin machine\n") {
		t.Fatalf("code %d calls %q: %s", code, calls, out)
	}
}

func TestTouchedXcodeSkipsWithoutIOSChange(t *testing.T) {
	root := touchedRepo(t, "README.md", "iosx/y.swift")
	touchedWrite(t, root, "ios/keep", "") // the iOS directory exists and the branch does not touch it
	code, out, calls := touchedXcode(root, "linux", "App")
	if code != 0 || len(calls) != 0 || out != "no package changed\nSKIP xcodebuild: no iOS change\n" {
		t.Fatalf("code %d calls %q out %q", code, calls, out)
	}
}

func TestTouchedXcodeNeedsScheme(t *testing.T) {
	root := touchedRepo(t, "ios/App/Main.swift", "a/x.ts")
	code, out, calls := touchedXcode(root, "darwin", "")
	if code != 2 || len(calls) != 0 {
		t.Fatalf("code %d calls %q: %s", code, calls, out)
	}
	// Settings without an iOS directory, or one outside the root, are usage errors, never a silent pass.
	for _, r := range []TouchedRun{{Scheme: "App"}, {IOSDir: "../ios"}} {
		var buf bytes.Buffer
		r.Root, r.Base, r.Out = root, "main", &buf
		if code := r.Run(); code != 2 {
			t.Fatalf("%+v: code %d: %s", r, code, buf.String())
		}
	}
}

// wantBadIOSDir runs the iOS proof with dir as the iOS directory and expects the usage error that names it.
func wantBadIOSDir(t *testing.T, root, dir string) {
	t.Helper()
	var out bytes.Buffer
	code := TouchedRun{Root: root, Base: "main", Out: &out, IOSDir: dir}.Run()
	if code != 2 || !strings.HasPrefix(out.String(), `proofs touched: bad iOS directory "`+dir+`": `) {
		t.Fatalf("%s: code %d: %s", dir, code, out.String())
	}
}

func TestTouchedIOSDirMissing(t *testing.T) {
	root := touchedRepo(t, "README.md")
	wantBadIOSDir(t, root, "ios")
	// A file is not a directory, and every element of a nested path is checked.
	wantBadIOSDir(t, root, "README.md")
	touchedWrite(t, root, "apps/ios/keep", "")
	wantBadIOSDir(t, root, "apps/mac")
	wantBadIOSDir(t, root, "app/ios")
}

func TestTouchedIOSDirWrongCase(t *testing.T) {
	root := touchedRepo(t, "README.md")
	touchedWrite(t, root, "ios/keep", "")
	wantBadIOSDir(t, root, "IOS")
	touchedWrite(t, root, "apps/ios/keep", "")
	wantBadIOSDir(t, root, "Apps/ios")
}

func TestTouchedIOSDirExists(t *testing.T) {
	root := touchedRepo(t, "README.md")
	touchedWrite(t, root, "ios/keep", "")
	touchedWrite(t, root, "apps/ios/keep", "")
	for _, dir := range []string{"ios", "./ios/", "apps/ios", "."} {
		if dir == "." { // the root itself: any change is an iOS change
			if err := (TouchedRun{IOSDir: dir}).checkIOSDir(root); err != nil {
				t.Fatal(err)
			}
			continue
		}
		code, out, calls := touchedXcodeDir(root, dir)
		if code != 0 || calls != 0 || out != "no package changed\nSKIP xcodebuild: no iOS change\n" {
			t.Fatalf("%s: code %d calls %d out %q", dir, code, calls, out)
		}
	}
}

// touchedXcodeDir runs the iOS proof with dir as the iOS directory; it returns the exit code, the output and the
// number of Exec calls.
func touchedXcodeDir(root, dir string) (code int, out string, calls int) {
	var buf bytes.Buffer
	code = TouchedRun{Root: root, Base: "main", Out: &buf, IOSDir: dir, Exec: func(string, []string) (int, error) {
		calls++
		return 0, nil
	}}.Run()
	return code, buf.String(), calls
}

func TestTouchedRunNoPackagesChanged(t *testing.T) {
	root := touchedRepo(t, "README.md")
	code, out, ran := touchedRun(root)
	if code != 0 || out != "no package changed\n" || len(ran) != 0 {
		t.Fatalf("code %d ran %v out %q", code, ran, out)
	}
}
