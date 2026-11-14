package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSmokeRender renders the testdata fixture with a real browser and ffmpeg. It runs only when FILM_RENDER_BROWSER
// points at a headless shell binary and ffmpeg and ffprobe are on PATH.
func TestSmokeRender(t *testing.T) {
	// Browser: FILM_RENDER_BROWSER, else the pinned shell when its zip is already in the user cache (no download in
	// a test). Otherwise the test is skipped, loudly.
	bin := os.Getenv("FILM_RENDER_BROWSER")
	if bin == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Skip("SKIPPED: no user cache dir and FILM_RENDER_BROWSER not set")
		}
		if _, err := os.Stat(filepath.Join(cache, "hivemill-film", "chrome-headless-shell-"+browserVersion+".zip")); err != nil {
			t.Skip("SKIPPED: no browser: set FILM_RENDER_BROWSER or cache the pinned zip")
		}
	}
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not on PATH", n)
		}
	}
	repo, _ := filepath.Abs("../../..")
	work := filepath.Join(t.TempDir(), "work")
	out := filepath.Join(t.TempDir(), "smoke.mp4")
	var so, se bytes.Buffer
	args := []string{"--repo", repo, "--storyboard", "testdata/smoke.json", "--scenes", "testdata/scenes",
		"--out", out, "--draft", "--work", work}
	if bin != "" {
		args = append(args, "--browser", bin)
	}
	code := run(args, &so, &se)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
	}
	lines := strings.Split(strings.TrimSpace(so.String()), "\n")
	if lines[len(lines)-1] != cleanupLine {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
	if _, err := os.Stat(work); err == nil {
		t.Fatal("work dir still exists")
	}
	if !strings.Contains(so.String(), "alpha") || !strings.Contains(so.String(), "frames/s") {
		t.Errorf("no per-scene progress with frames/second in stdout:\n%s", so.String())
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=width,height,codec_name,r_frame_rate", "-of", "default=nw=1", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codec_name=h264", "width=1920", "height=1080", "r_frame_rate=30/1", "duration=4.0"} {
		if !strings.Contains(string(probe), want) {
			t.Errorf("ffprobe output lacks %s:\n%s", want, probe)
		}
	}
}
