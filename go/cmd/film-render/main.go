// Command film-render turns a storyboard of designed HTML scenes into an MP4: a headless Chrome shell is driven over
// the DevTools pipe, every frame is captured as a PNG and streamed into one ffmpeg process.
//
//	film-render [--repo DIR] [--storyboard FILE] [--scenes DIR] --out PATH.mp4 [--draft] [--browser PATH] [--work DIR]
//
// Exit codes: 0 ok, 1 failure, 2 usage, 3 refused by a guard (checked before any browser or ffmpeg starts: film
// length, ids and banned words, placeholders in final mode, --out inside the repo).
//
// Scene contract (scene pages are built to this):
//   - Each non-placeholder scene is <scenes>/<id>.html, loaded as a file:// URL at the storyboard width x height
//     with device pixel ratio 1.
//   - The page sets window.filmReady = true once its fonts and images are loaded. The renderer waits for it (30 s,
//     then exit 1).
//   - The page implements window.seek(t), t = seconds from the start of the scene. It is a pure function of t, may
//     return a Promise, and does its own transitions. For frame k the renderer awaits seek(k/fps) and then captures.
//   - The renderer, not the scene, draws the caption: one bottom band (element id film-caption, 140 px high, Inter
//     from docs/demo/fonts/, white on a translucent dark band) holding the storyboard caption. Do not draw captions.
//   - In --draft a placeholder scene renders a built-in card reading "PLACEHOLDER: herdr-core view (owner to choose)".
//
// Frames are the scene length in seconds times fps, rounded, per scene; scenes are concatenated without crossfades.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const cleanupLine = "cleanup: work dir removed, children stopped"

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func refusef(f string, a ...any) error { return &exitError{3, fmt.Sprintf(f, a...)} }
func failf(f string, a ...any) error   { return &exitError{1, fmt.Sprintf(f, a...)} }

type options struct {
	repo, storyboard, scenes, out, browser, work string
	draft                                        bool
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("film-render", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &options{}
	fs.StringVar(&o.repo, "repo", "", "repository root (default: git rev-parse --show-toplevel of the cwd)")
	fs.StringVar(&o.storyboard, "storyboard", "", "storyboard JSON (default: <repo>/docs/film/storyboard.json)")
	fs.StringVar(&o.scenes, "scenes", "", "scene pages directory (default: <repo>/docs/film/scenes)")
	fs.StringVar(&o.out, "out", "", "output MP4 path, outside the repo (required)")
	fs.BoolVar(&o.draft, "draft", false, "allow placeholder scenes (renders a placeholder card)")
	fs.StringVar(&o.browser, "browser", "", "headless shell binary (default: the pinned Chrome for Testing 131.0.6778.204, cached)")
	fs.StringVar(&o.work, "work", "", "work dir for the unzipped browser and profile (default: under the user cache dir; removed afterwards)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "usage: film-render [--repo DIR] [--storyboard FILE] [--scenes DIR] --out PATH.mp4 [--draft] [--browser PATH] [--work DIR]")
			fs.VisitAll(func(f *flag.Flag) { fmt.Fprintf(stdout, "  --%s\n    \t%s\n", f.Name, f.Usage) })
			return 0
		}
		fmt.Fprintf(stderr, "film-render: %v\nusage: film-render [--repo DIR] [--storyboard FILE] [--scenes DIR] --out PATH.mp4 [--draft] [--browser PATH] [--work DIR]\n", err)
		return 2
	}
	if o.out == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "film-render: usage: --out PATH.mp4 is required and no positional arguments are accepted (see --help)")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	k := &kids{}
	err := execute(ctx, o, k, stdout, stderr)
	k.cleanup()
	fmt.Fprintln(stdout, cleanupLine)
	if err == nil {
		return 0
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "film-render: interrupted")
		return 1
	}
	var ee *exitError
	if errors.As(err, &ee) {
		fmt.Fprintf(stderr, "film-render: %s\n", ee.msg)
		return ee.code
	}
	fmt.Fprintf(stderr, "film-render: %v\n", err)
	return 1
}

// kids tracks everything that must be gone when the run ends.
type kids struct {
	procs   []*proc
	workDir string
	dropOut string // partial output removed after the children stop
}

func (k *kids) cleanup() {
	for i := len(k.procs) - 1; i >= 0; i-- {
		k.procs[i].stop()
	}
	if k.dropOut != "" {
		os.Remove(k.dropOut)
	}
	if k.workDir != "" {
		os.RemoveAll(k.workDir)
	}
}

func execute(ctx context.Context, o *options, k *kids, stdout, stderr io.Writer) error {
	if o.repo == "" {
		b, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return failf("cannot find the repo: git rev-parse failed (%v). Pass --repo DIR.", err)
		}
		o.repo = strings.TrimSpace(string(b))
	}
	if o.storyboard == "" {
		o.storyboard = filepath.Join(o.repo, "docs", "film", "storyboard.json")
	}
	if o.scenes == "" {
		o.scenes = filepath.Join(o.repo, "docs", "film", "scenes")
	}
	sb, err := loadStoryboard(o.storyboard)
	if err != nil {
		return err
	}
	if err := checkGuards(sb, o); err != nil {
		return err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return failf("ffmpeg not found on PATH. Install it yourself; film-render installs nothing.")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return failf("ffprobe not found on PATH. Install it yourself; film-render installs nothing.")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return failf("no user cache dir: %v", err)
	}
	cache = filepath.Join(cache, "hivemill-film")
	work := o.work
	if work == "" {
		work = filepath.Join(cache, fmt.Sprintf("work-%d", os.Getpid()))
	}
	if err := prepareWork(work); err != nil {
		return err
	}
	k.workDir = work

	bin := o.browser
	if bin == "" {
		zpath, err := ensureZip(ctx, cache)
		if err != nil {
			return err
		}
		if bin, err = unzipTo(zpath, filepath.Join(work, "chrome")); err != nil {
			return err
		}
	}
	if o.out, err = filepath.Abs(o.out); err != nil {
		return failf("cannot resolve --out: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.out), 0o755); err != nil {
		return failf("cannot create the output directory: %v", err)
	}

	bp, w, r, err := launchBrowser(bin, filepath.Join(work, "profile"), sb.Width, sb.Height)
	if err != nil {
		return err
	}
	k.procs = append(k.procs, bp)
	defer w.Close()
	defer r.Close()
	client := newCDP(w, r, 60*time.Second)
	sess, err := openSession(ctx, client, sb.Width, sb.Height)
	if err != nil {
		return failf("browser setup failed: %v", err)
	}
	enc, err := startEncoder(ffmpeg, sb.FPS, o.out)
	if err != nil {
		return err
	}
	k.procs = append(k.procs, enc.p)

	if err := renderFilm(ctx, sess, enc, sb, o, work, stdout, stderr); err != nil {
		k.dropOut = o.out
		return err
	}
	if err := enc.finish(); err != nil {
		k.dropOut = o.out
		return err
	}
	d, err := probeDuration(ffprobe, o.out)
	if err != nil {
		return err
	}
	if d > maxFilmSeconds {
		k.dropOut = o.out
		return refusef("guard length: the rendered film is %.2f s, over %d s; %s removed.", d, maxFilmSeconds, o.out)
	}
	fmt.Fprintf(stdout, "wrote %s (%.2f s)\n", o.out, d)
	return nil
}

// prepareWork makes the work dir, refusing to adopt (and later delete) a directory that already has content.
func prepareWork(work string) error {
	ents, err := os.ReadDir(work)
	switch {
	case err == nil && len(ents) > 0:
		return refusef("--work %s is not empty; it is deleted at the end, so pass an empty or new directory.", work)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return failf("cannot use --work %s: %v", work, err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return failf("cannot create work dir %s: %v", work, err)
	}
	return nil
}
