package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// TestShimPinsMatchReproducibleBuild rebuilds hivemill-install for every release target with exactly the flags
// of .github/workflows/release.yml and compares the sha256 with the pins in plugins/hivemill/scripts/install.
// release.yml fails closed on a mismatch, so this catches a Go change without a pin bump before the tag does.
func TestShimPinsMatchReproducibleBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-builds three targets")
	}
	shim, err := os.ReadFile(filepath.Join("..", "..", "..", "plugins", "hivemill", "scripts", "install"))
	if err != nil {
		t.Fatal(err)
	}
	targets := []struct{ goos, goarch, pin string }{
		{"linux", "amd64", "SHA_LINUX_AMD64"},
		{"linux", "arm64", "SHA_LINUX_ARM64"},
		{"darwin", "arm64", "SHA_DARWIN_ARM64"},
	}
	for _, tg := range targets {
		m := regexp.MustCompile(`(?m)^` + tg.pin + `="([0-9a-f]{64})"$`).FindSubmatch(shim)
		if m == nil {
			t.Fatalf("%s is missing or not 64 hex chars in plugins/hivemill/scripts/install", tg.pin)
		}
		out := filepath.Join(t.TempDir(), "hivemill-install")
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", out, "./cmd/hivemill-install")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+tg.goos, "GOARCH="+tg.goarch)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s/%s: %v\n%s", tg.goos, tg.goarch, err, b)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != string(m[1]) {
			t.Errorf("%s is stale: go/cmd/hivemill-install or go/internal/install changed without a pin bump.\n"+
				"  rebuilt %s/%s sha256 %s\n  shim pin %s\nSet %s=%q in plugins/hivemill/scripts/install (release.yml flags, see docs/RELEASING.md).",
				tg.pin, tg.goos, tg.goarch, got, string(m[1]), tg.pin, got)
		}
	}
}
