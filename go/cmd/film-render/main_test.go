package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type env struct {
	repo, scenes, out, sbPath string
}

// setup makes a repo dir, a scenes dir with a.html and b.html, and an empty PATH so a run that passes every guard
// stops at "ffmpeg not found" (exit 1) instead of starting a browser.
func setup(t *testing.T) *env {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	e := &env{repo: t.TempDir(), scenes: t.TempDir(), out: filepath.Join(t.TempDir(), "film.mp4")}
	for _, n := range []string{"a", "b"} {
		e.page(t, n, "<html><body><p>Plain scene "+n+"</p></body></html>")
	}
	e.sbPath = filepath.Join(t.TempDir(), "storyboard.json")
	return e
}

func (e *env) page(t *testing.T, id, html string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.scenes, id+".html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) board(t *testing.T, maxSec float64, scenes ...map[string]any) {
	t.Helper()
	sb := map[string]any{"title": "Test film", "width": 1920, "height": 1080, "fps": 30, "max_seconds": maxSec, "scenes": scenes}
	data, _ := json.Marshal(sb)
	if err := os.WriteFile(e.sbPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sc(id string, start, end float64, kv ...any) map[string]any {
	m := map[string]any{"id": id, "start": start, "end": end, "visual": "v", "caption": "c", "onscreen": []string{}, "sources": []string{}, "placeholder": false}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func (e *env) run(extra ...string) (int, string, string) {
	args := append([]string{"--repo", e.repo, "--storyboard", e.sbPath, "--scenes", e.scenes, "--out", e.out}, extra...)
	var so, se bytes.Buffer
	code := run(args, &so, &se)
	return code, so.String(), se.String()
}

const (
	refused = 3
	passed  = 1 // every guard passed; the empty PATH then stops the run with "ffmpeg not found"
)

func expect(t *testing.T, e *env, want int, wantErr string, extra ...string) {
	t.Helper()
	code, so, se := e.run(extra...)
	if code != want {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, want, so, se)
	}
	if !strings.Contains(se, wantErr) {
		t.Fatalf("stderr %q does not contain %q", se, wantErr)
	}
	if !strings.HasSuffix(strings.TrimSpace(so), cleanupLine) {
		t.Fatalf("last stdout line must be the cleanup line, got %q", so)
	}
}

func TestGuardLength(t *testing.T) {
	cases := []struct {
		name   string
		max    float64
		scenes []map[string]any
		want   int
		msg    string
	}{
		{"61 s film refused", 61, []map[string]any{sc("a", 0, 30), sc("b", 30, 61)}, refused, "guard length"},
		{"ends past max_seconds refused", 60, []map[string]any{sc("a", 0, 30), sc("b", 30, 61)}, refused, "guard length"},
		{"60 s film passes", 60, []map[string]any{sc("a", 0, 30), sc("b", 30, 60)}, passed, "ffmpeg"},
		{"gap between scenes refused", 60, []map[string]any{sc("a", 0, 10), sc("b", 12, 20)}, refused, "contiguous"},
		{"backwards scene refused", 60, []map[string]any{sc("a", 0, 10), sc("b", 10, 9)}, refused, "not after its start"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			e.board(t, c.max, c.scenes...)
			expect(t, e, c.want, c.msg)
		})
	}
}

func TestGuardBannedWords(t *testing.T) {
	cases := []struct {
		name string
		sc   map[string]any
		html string
		want int
		msg  string
	}{
		{"id in caption refused", sc("a", 0, 5, "caption", "Closes T-0001 today"), "", refused, "T-0001"},
		{"version v0.4.0 passes", sc("a", 0, 5, "caption", "Tagged v0.4.0"), "", passed, "ffmpeg"},
		{"range 8.5-9x passes", sc("a", 0, 5, "caption", "8.5–9× faster guard"), "", passed, "ffmpeg"},
		{"8 ms and 69 ms pass", sc("a", 0, 5, "caption", "8 ms against 69 ms"), "", passed, "ffmpeg"},
		{"M number in onscreen refused", sc("a", 0, 5, "onscreen", []string{"see M1234"}), "", refused, "onscreen"},
		{"hash number in visual refused", sc("a", 0, 5, "visual", "item #12"), "", refused, "visual"},
		{"v0.5 in caption refused", sc("a", 0, 5, "caption", "v0.5.0 is out"), "", refused, "v0.5"},
		{"predicted word refused", sc("a", 0, 5, "caption", "Predicted gains"), "", refused, "predicted"},
		{"ticket in page text refused", sc("a", 0, 5), "<body><h1>Open Ticket</h1></body>", refused, "page text"},
		{"ticket in page script or css passes", sc("a", 0, 5), "<style>a{color:#123456}</style><script>var ticket=1</script><p>ok</p>", passed, "ffmpeg"},
		{"PR 633 in a caption refused", sc("a", 0, 5, "caption", "PR 633 adds a tab"), "", refused, "caption"},
		{"PR 633 in a non-placeholder visual refused", sc("a", 0, 5, "visual", "PR 633 view"), "", refused, "visual"},
		{"scene id with a path refused", sc("../a", 0, 5), "", refused, "scene-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			if c.html != "" {
				e.page(t, "a", c.html)
			}
			e.board(t, 60, c.sc)
			expect(t, e, c.want, c.msg)
		})
	}
}

func TestGuardPlaceholder(t *testing.T) {
	ph := sc("herdr", 5, 10, "placeholder", true, "visual", "herdr-core view, PR 633 adds the tab")
	t.Run("PR 633 in a placeholder visual passes", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5), ph)
		expect(t, e, passed, "ffmpeg", "--draft")
	})
	t.Run("final mode refuses and names the scene", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5), ph)
		expect(t, e, refused, `"herdr"`)
	})
	t.Run("draft passes", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5), ph)
		expect(t, e, passed, "ffmpeg", "--draft")
	})
}

func TestGuardOutPath(t *testing.T) {
	t.Run("out inside repo refused", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5))
		e.out = filepath.Join(e.repo, "build", "film.mp4")
		expect(t, e, refused, "inside the repo")
	})
	t.Run("out through a symlinked dir refused", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5))
		inside := filepath.Join(e.repo, "out")
		if err := os.Mkdir(inside, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(inside, link); err != nil {
			t.Fatal(err)
		}
		e.out = filepath.Join(link, "film.mp4")
		expect(t, e, refused, "inside the repo")
	})
	t.Run("out as a dangling symlink into the repo refused", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5))
		link := filepath.Join(t.TempDir(), "film.mp4")
		if err := os.Symlink(filepath.Join(e.repo, "docs", "film.mp4"), link); err != nil {
			t.Fatal(err)
		}
		e.out = link
		expect(t, e, refused, "inside the repo")
	})
	t.Run("out in a temp dir passes", func(t *testing.T) {
		e := setup(t)
		e.board(t, 60, sc("a", 0, 5))
		expect(t, e, passed, "ffmpeg")
	})
}

func TestMissingFFmpegNamesIt(t *testing.T) {
	e := setup(t)
	e.board(t, 60, sc("a", 0, 5))
	code, _, se := e.run()
	if code != 1 || !strings.Contains(se, "ffmpeg not found on PATH") || !strings.Contains(se, "installs nothing") {
		t.Fatalf("exit %d, stderr %q", code, se)
	}
	if _, err := os.Stat(e.out); err == nil {
		t.Fatal("no output file may exist")
	}
}

func TestUsage(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want int
		out  string
	}{
		{"no --out", []string{"--draft"}, 2, ""},
		{"bogus flag", []string{"--bogus"}, 2, ""},
		{"positional arg", []string{"--out", "x.mp4", "extra"}, 2, ""},
		{"help", []string{"--help"}, 0, "--out"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var so, se bytes.Buffer
			if code := run(c.args, &so, &se); code != c.want {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.want, se.String())
			}
			if c.name == "help" {
				for _, f := range []string{"--out", "--draft", "--browser", "--storyboard", "--scenes", "--repo", "--work"} {
					if !strings.Contains(so.String(), f) {
						t.Errorf("help lacks %s:\n%s", f, so.String())
					}
				}
			}
		})
	}
}

func TestMissingStoryboardFails(t *testing.T) {
	e := setup(t)
	code, _, se := e.run()
	if code != 1 || !strings.Contains(se, "cannot read storyboard") {
		t.Fatalf("exit %d, stderr %q", code, se)
	}
}

func TestWorkDirNotEmptyRefused(t *testing.T) {
	e := setup(t)
	e.board(t, 60, sc("a", 0, 5))
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubs := t.TempDir() // stand-in ffmpeg and ffprobe so the run reaches the work dir check
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(stubs, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", stubs)
	code, _, se := e.run("--work", work)
	if code != 3 || !strings.Contains(se, "not empty") {
		t.Fatalf("exit %d, stderr %q", code, se)
	}
	if _, err := os.Stat(filepath.Join(work, "keep")); err != nil {
		t.Fatalf("a non-empty --work must never be deleted: %v", err)
	}
}
