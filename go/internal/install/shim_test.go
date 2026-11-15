package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const realShim = "../../../plugins/hivemill/scripts/install"

// shimSource returns the verifier shim. A test may read a repo file outside go/ only through the real file or a
// copy under testdata (the merged-tree Go proof extracts go/ alone); when both exist they must be identical.
func shimSource(t *testing.T) []byte {
	t.Helper()
	copyBytes, err := os.ReadFile(filepath.Join("testdata", "install-shim"))
	if err != nil {
		t.Fatalf("testdata copy of the shim: %v", err)
	}
	if real, err := os.ReadFile(realShim); err == nil && !bytes.Equal(real, copyBytes) {
		t.Fatal("testdata/install-shim differs from plugins/hivemill/scripts/install: copy it over")
	}
	return copyBytes
}

func shimPlatform(t *testing.T) string {
	t.Helper()
	plat := runtime.GOOS + "-" + runtime.GOARCH
	switch plat {
	case "linux-amd64", "linux-arm64", "darwin-arm64":
	default:
		t.Skipf("the shim has no build for %s", plat)
	}
	for _, tool := range []string{"curl", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("neither sha256sum nor shasum is installed")
		}
	}
	return plat
}

type shimRun struct {
	dir, marker, assetDir string
	plat                  string
}

// newShim installs the shim in a temp plugin tree with the given pinned hash and an asset that records its run.
func newShim(t *testing.T, pinned string, assetBody string) *shimRun {
	t.Helper()
	plat := shimPlatform(t)
	dir := t.TempDir()
	r := &shimRun{dir: dir, marker: filepath.Join(dir, "marker"), assetDir: filepath.Join(dir, "release"), plat: plat}
	mustMkdir(t, filepath.Join(dir, "plugin", "scripts"))
	mustMkdir(t, r.assetDir)
	asset := []byte(assetBody)
	mustWrite(t, filepath.Join(r.assetDir, "hivemill-install-"+plat), assetBody)
	sum := sha256.Sum256(asset)
	if pinned == "" {
		pinned = hex.EncodeToString(sum[:])
	}
	src := regexp.MustCompile(`(?m)^(SHA_[A-Z0-9_]+)="[0-9a-f]{64}"$`).ReplaceAll(shimSource(t), []byte(`$1="`+pinned+`"`))
	if err := os.WriteFile(filepath.Join(dir, "plugin", "scripts", "install"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *shimRun) run(t *testing.T, base string, args ...string) (int, string, string) {
	t.Helper()
	if base == "" {
		base = "file://" + r.assetDir
	}
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(r.dir, "plugin", "scripts", "install")}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.dir, "XDG_CACHE_HOME=" + filepath.Join(r.dir, "cache"),
		"HIVEMILL_INSTALL_BASE_URL=" + base, "MARKER=" + r.marker}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return rc, out.String(), errb.String()
}

func (r *shimRun) executed() bool { _, err := os.Stat(r.marker); return err == nil }

const markerAsset = "#!/bin/sh\ntouch \"$MARKER\"\necho \"ran: $*\"\necho \"root=$HIVEMILL_PLUGIN_ROOT\"\n"

func TestShimRunsAVerifiedAssetWithAllArguments(t *testing.T) {
	r := newShim(t, "", markerAsset)
	rc, out, errb := r.run(t, "", "--dry-run", "--ref", "v1 2")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb)
	}
	if !r.executed() || !strings.Contains(out, "ran: --dry-run --ref v1 2") {
		t.Errorf("the verified asset did not run with its arguments: %q", out)
	}
	if want := "root=" + filepath.Join(r.dir, "plugin"); !strings.Contains(out, want) {
		t.Errorf("the plugin root is not exported: %q (want %s)", out, want)
	}
}

// Acceptance 4: an asset whose sha256 differs from the pinned one is never executed.
func TestShimRefusesAHashMismatch(t *testing.T) {
	r := newShim(t, strings.Repeat("a", 64), markerAsset)
	rc, out, errb := r.run(t, "", "--dry-run")
	if rc == 0 {
		t.Fatal("a mismatching asset was accepted")
	}
	if !strings.Contains(errb, "sha256 mismatch") || !strings.Contains(errb, strings.Repeat("a", 64)) {
		t.Errorf("stderr does not name the mismatch: %q", errb)
	}
	if r.executed() || out != "" {
		t.Errorf("the asset ran: marker=%v out=%q", r.executed(), out)
	}
	cached := filepath.Join(r.dir, "cache", "hivemill-install")
	if _, err := os.Stat(cached); err == nil {
		var left []string
		_ = filepath.Walk(cached, func(p string, info os.FileInfo, err error) error { //nolint:errcheck // listing for the failure message only
			if err == nil && !info.IsDir() {
				left = append(left, p)
			}
			return nil
		})
		if len(left) != 0 {
			t.Errorf("an unverified download was kept: %v", left)
		}
	}
}

// The default pin is 64 zeros: an unpinned plugin copy fails closed.
func TestShimUnpinnedCopyFailsClosed(t *testing.T) {
	r := newShim(t, strings.Repeat("0", 64), markerAsset)
	if rc, _, errb := r.run(t, ""); rc == 0 || r.executed() || !strings.Contains(errb, "sha256 mismatch") {
		t.Errorf("rc=%d executed=%v stderr=%q", rc, r.executed(), errb)
	}
	if !bytes.Contains(shimSource(t), []byte(`SHA_LINUX_AMD64="`+strings.Repeat("0", 64)+`"`)) &&
		!regexp.MustCompile(`SHA_LINUX_AMD64="[0-9a-f]{64}"`).Match(shimSource(t)) {
		t.Error("the shim has no pinned linux-amd64 value")
	}
}

func TestShimFailsWhenTheAssetCannotBeDownloaded(t *testing.T) {
	r := newShim(t, "", markerAsset)
	if err := os.Remove(filepath.Join(r.assetDir, "hivemill-install-"+r.plat)); err != nil {
		t.Fatal(err)
	}
	if rc, _, errb := r.run(t, ""); rc == 0 || r.executed() || !strings.Contains(errb, "cannot download") {
		t.Errorf("rc=%d executed=%v stderr=%q", rc, r.executed(), errb)
	}
}

// A cached binary that no longer matches the pin is not run, even if the download is then gone.
func TestShimNeverRunsATamperedCache(t *testing.T) {
	r := newShim(t, "", markerAsset)
	if rc, _, errb := r.run(t, ""); rc != 0 {
		t.Fatalf("first run: %s", errb)
	}
	if err := os.Remove(r.marker); err != nil {
		t.Fatal(err)
	}
	var cached string
	_ = filepath.Walk(filepath.Join(r.dir, "cache"), func(p string, info os.FileInfo, err error) error { //nolint:errcheck // locating the cache for the test
		if err == nil && !info.IsDir() && strings.HasSuffix(p, "hivemill-install-"+r.plat) {
			cached = p
		}
		return nil
	})
	if cached == "" {
		t.Fatal("no cached asset")
	}
	mustWrite(t, cached, "#!/bin/sh\ntouch \"$MARKER\"\necho tampered\n")
	if err := os.Remove(filepath.Join(r.assetDir, "hivemill-install-"+r.plat)); err != nil {
		t.Fatal(err)
	}
	if rc, out, _ := r.run(t, ""); rc == 0 || r.executed() || strings.Contains(out, "tampered") {
		t.Errorf("the tampered cache ran: rc=%d out=%q", rc, out)
	}
}

func TestShimRefusesOtherDownloadSchemes(t *testing.T) {
	r := newShim(t, "", markerAsset)
	for _, base := range []string{"http://example.test/x", "ftp://example.test/x", "/tmp/x"} {
		if rc, _, errb := r.run(t, base); rc == 0 || r.executed() || !strings.Contains(errb, "refusing a download base") {
			t.Errorf("%s: rc=%d executed=%v stderr=%q", base, rc, r.executed(), errb)
		}
	}
}

func TestShimStaysSmallAndPinsEveryPlatform(t *testing.T) {
	src := string(shimSource(t))
	if n := strings.Count(src, "\n"); n > 80 {
		t.Errorf("the shim has %d lines, at most 80 allowed", n)
	}
	if !strings.HasPrefix(src, "#!/bin/sh\n") {
		t.Error("the shim must be POSIX sh")
	}
	for _, v := range []string{"SHA_LINUX_AMD64", "SHA_LINUX_ARM64", "SHA_DARWIN_ARM64"} {
		if !regexp.MustCompile(`(?m)^` + v + `="[0-9a-f]{64}"$`).MatchString(src) {
			t.Errorf("%s is not a 64-hex pin", v)
		}
	}
	for _, bad := range []string{"python", "eval ", "| sh", "| bash"} {
		if strings.Contains(src, bad) {
			t.Errorf("the shim contains %q", bad)
		}
	}
}
