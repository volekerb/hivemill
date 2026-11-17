package installer

// Port of the subprocess-driven cases of tests/test_demo_tour.py (46 Python cases, prefix TestDemoTour).
//
// There is no Go port of ci/demo_tour.py or ci/demo_cdp.py (grep of go/ finds neither), so the Python script is the
// product: the cases below run `python3 -I ci/demo_tour.py ...` as a subprocess, as the Python Cli cases do, and skip
// when python3 is not on PATH (the Env.NeedPython semantics; no factory home is needed, so no ft.Env is built).
//
// Ported (9): Cli x5, SceneAndScale.test_unknown_scene_is_a_usage_error, SceneAndScale.test_scale_outside_range_is_a_usage_error,
// DemoDir.test_foreign_non_empty_dir_is_refused_and_kept, Integration.test_two_pages_three_frames_each (DEMO_BROWSER).
//
// Not ported (37): every one of these imports ci/demo_tour.py or ci/demo_cdp.py and calls its functions in-process
// (scroll_y, fade, shots, record_tour, check_pages, load_tour, prepare_demo_dir, serve_demo, main, ...), most with
// mock.patch on module attributes, a FakeBrowser object or a Python http.server; a subprocess cannot reach any of that.
//
//	not ported: Maths.test_scroll_starts_at_top_and_ends_at_target - calls demo_tour.scroll_y in-process
//	not ported: Maths.test_scroll_target_is_capped_by_page_height - calls demo_tour.scroll_y in-process
//	not ported: Maths.test_scroll_is_monotonic_and_eased - calls demo_tour.scroll_y in-process
//	not ported: Maths.test_fade_is_zero_at_page_edges_and_one_inside - calls demo_tour.fade in-process
//	not ported: Maths.test_caption_lookup - calls demo_tour.caption_at in-process
//	not ported: PageOptions.test_defaults_match_the_old_behaviour - calls scroll_y/scroll_target/ease/fade/push_scale/shots in-process
//	not ported: PageOptions.test_hold_keeps_the_page_still_then_scrolls_to_the_end - calls scroll_y in-process
//	not ported: PageOptions.test_scroll_to_px_beats_the_default_cap_but_not_the_page_end - calls scroll_target in-process
//	not ported: PageOptions.test_fade_in_false_is_visible_on_frame_zero_and_still_fades_out - calls fade in-process
//	not ported: PageOptions.test_push_in_scales_from_one_to_the_end_scale - calls push_scale in-process
//	not ported: PageOptions.test_bad_options_are_refused_with_the_page_named - calls shots and asserts on demo_tour.PageError in-process
//	not ported: TourOptions.test_per_page_duration_decides_the_frame_count - record_tour with an in-process FakeBrowser and a Python stub server
//	not ported: TourOptions.test_hold_then_scroll_to_px_and_hard_open - record_tour with an in-process FakeBrowser
//	not ported: TourOptions.test_selector_scroll_target - record_tour with an in-process FakeBrowser
//	not ported: TourOptions.test_unknown_selector_is_a_page_error - record_tour with an in-process FakeBrowser, asserts PageError
//	not ported: TourOptions.test_default_page_fades_in_from_the_background - record_tour with an in-process FakeBrowser
//	not ported: TourOptions.test_push_in_reaches_the_end_scale - record_tour with an in-process FakeBrowser
//	not ported: TourOptions.test_every_page_is_frozen_before_its_first_frame - record_tour with an in-process FakeBrowser (records CDP calls)
//	not ported: TourOptions.test_max_frames_cap_applies_per_page - record_tour with an in-process FakeBrowser
//	not ported: TourOptions.test_labels_come_from_the_page_object_before_the_list - calls shots in-process
//	not ported: Layout.test_caption_bar_starts_below_the_page_box - reads demo_tour.caption_top/page_height in-process
//	not ported: Layout.test_page_box_css_stops_where_the_caption_band_starts - reads demo_tour module constants (PAGE_CSS, CAPTION_CSS)
//	not ported: Layout.test_page_is_zoomed_so_the_column_fills_the_frame - reads demo_tour module constants (ZOOM, ZOOM_CSS)
//	not ported: Layout.test_label_sits_in_the_caption_band - reads demo_tour.LABEL_CSS and helpers in-process
//	not ported: Storyboard.test_pages_and_labels_come_from_storyboard - calls demo_tour.load_tour in-process
//	not ported: NonOk.test_ok_page_passes - calls demo_tour.check_pages in-process
//	not ported: NonOk.test_non_200_names_the_path - calls check_pages in-process, asserts PageError
//	not ported: NonOk.test_record_tour_stops_before_browsing_on_non_200 - record_tour with mock.Mock browser
//	not ported: NonOk.test_unreachable_server_is_a_page_error - calls check_pages in-process, asserts PageError
//	not ported: ServeFailure.test_server_is_stopped_when_serve_gives_up - mock.patch of demo_cdp.screenshots.serve/CHILDREN and demo_cdp.stop
//	not ported: InterruptedStartup.test_interrupt_during_serve_stops_the_server - mock.patch of serve/seed_demo/signal, calls demo_tour.main in-process
//	not ported: InterruptedStartup.test_interrupt_during_seed_stops_the_seed - mock.patch of serve/seed_demo/signal, calls demo_tour.main in-process
//	not ported: SceneAndScale.test_load_tour_picks_the_named_ui_scene - mock.patch.object(demo_tour, "DEMO") and load_tour in-process
//	not ported: SceneAndScale.test_repo_storyboard_ui_scenes_are_valid_shots - calls load_tour and shots in-process
//	not ported: DemoDir.test_marked_dir_is_rebuilt_and_empty_dir_accepted - calls demo_tour.prepare_demo_dir in-process
//	not ported: ServeEnv.test_server_runs_with_demo_home_not_the_real_one - mock.patch of demo_cdp.screenshots.spawn/urlopen, calls serve_demo in-process
//	not ported: Integration.test_selector_target_clears_a_sticky_title_bar - drives demo_cdp.Browser and demo_tour._inject_js/selector_top in-process (also needs DEMO_BROWSER)

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dtRAMReason = "work dir on RAM tmpfs: demo scripts refuse paths under /tmp, /dev/shm, /var/tmp"

func dtRepo(t testing.TB) string { return plHmRepo(t) }

func dtNeedPython(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH: ci/demo_tour.py has no Go port, this test runs it as a subprocess; it runs where python3 exists")
	}
	return p
}

// dtSkipOnRAM is envcheck.skip_on_ram(): skip when docs/demo/out resolves to or under /tmp, /dev/shm or /var/tmp.
func dtSkipOnRAM(t testing.TB) {
	t.Helper()
	// docs/demo/out may not exist: resolve its closest existing parent, as Path.resolve does for a missing tail.
	p := filepath.Join(dtRepo(t), "docs", "demo", "out")
	tail := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = filepath.Join(r, tail)
			break
		}
		tail = filepath.Join(filepath.Base(p), tail)
		p = filepath.Dir(p)
	}
	for _, ram := range []string{"/tmp", "/dev/shm", "/var/tmp"} {
		r := ram
		if rr, err := filepath.EvalSymlinks(ram); err == nil {
			r = rr
		}
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			t.Skip(dtRAMReason)
		}
	}
}

type dtResult struct {
	code           int
	stdout, stderr string
}

// dtRunTour runs `python3 -I ci/demo_tour.py args...` (60 s limit unless timeout is given).
func dtRunTour(t testing.TB, timeout time.Duration, args ...string) dtResult {
	t.Helper()
	py := dtNeedPython(t)
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, append([]string{"-I", filepath.Join(dtRepo(t), "ci", "demo_tour.py")}, args...)...)
	cmd.WaitDelay = 5 * time.Second
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run demo_tour.py: %v", err)
		}
		code = ee.ExitCode()
	}
	return dtResult{code, so.String(), se.String()}
}

func dtWant(t testing.TB, r dtResult, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
}

func dtOut(t testing.TB, rel string) string {
	return filepath.Join(dtRepo(t), "docs", "demo", "out", rel)
}

// ---- Cli --------------------------------------------------------------------------------------------------------

func TestDemoTourCliTmpOutIsRefused(t *testing.T) {
	r := dtRunTour(t, 0, "--out", "/tmp/x", "--max-frames", "1")
	dtWant(t, r, 3)
	if !strings.Contains(r.stderr, "/tmp") {
		t.Errorf("stderr lacks /tmp: %s", r.stderr)
	}
}

func TestDemoTourCliTmpWorkIsRefused(t *testing.T) {
	dtWant(t, dtRunTour(t, 0, "--out", dtOut(t, "x"), "--work", "/tmp/w"), 3)
}

func TestDemoTourCliTmpDemoDirIsRefused(t *testing.T) {
	dtWant(t, dtRunTour(t, 0, "--out", dtOut(t, "x"), "--demo-dir", "/tmp/d"), 3)
}

func TestDemoTourCliMaxFramesBelowPageCountIsUsageError(t *testing.T) {
	dtSkipOnRAM(t)
	r := dtRunTour(t, 0, "--out", dtOut(t, "x"), "--demo-dir", dtOut(t, "x-demo"), "--pages", "/a,/b,/c", "--max-frames", "2")
	dtWant(t, r, 2)
	if !strings.Contains(r.stderr, "at least the number of pages") {
		t.Errorf("stderr: %s", r.stderr)
	}
}

func TestDemoTourCliBadPagesIsUsageError(t *testing.T) {
	dtSkipOnRAM(t)
	dtWant(t, dtRunTour(t, 0, "--out", dtOut(t, "x"), "--demo-dir", dtOut(t, "x-demo"), "--pages", "goals"), 2)
}

// ---- SceneAndScale ----------------------------------------------------------------------------------------------

// dtCli is SceneAndScale.cli: an explicit --demo-dir in the checkout (never created on a usage error): the default is
// under $HOME, which the local gate puts in its run dir under /var/tmp (T-1679), a dir the demo scripts refuse as RAM-backed.
func dtCli(t testing.TB, args ...string) dtResult {
	t.Helper()
	return dtRunTour(t, 0, append([]string{"--out", dtOut(t, "t"), "--demo-dir", dtOut(t, "t-demo")}, args...)...)
}

func TestDemoTourSceneAndScaleUnknownSceneIsAUsageError(t *testing.T) {
	dtSkipOnRAM(t)
	r := dtCli(t, "--scene", "nope")
	dtWant(t, r, 2)
	if !strings.Contains(r.stderr, "'nope'") {
		t.Errorf("stderr lacks 'nope': %s", r.stderr)
	}
	if !strings.Contains(r.stderr, "open, work, result") { // lists the ui scenes that exist
		t.Errorf("stderr lacks the scene list: %s", r.stderr)
	}
}

func TestDemoTourSceneAndScaleScaleOutsideRangeIsAUsageError(t *testing.T) {
	dtSkipOnRAM(t)
	for _, bad := range []string{"0.5", "3"} {
		r := dtCli(t, "--scale", bad)
		dtWant(t, r, 2)
		if !strings.Contains(r.stderr, "--scale") {
			t.Errorf("--scale %s: stderr lacks --scale: %s", bad, r.stderr)
		}
	}
}

// ---- DemoDir ----------------------------------------------------------------------------------------------------

func TestDemoTourDemoDirForeignNonEmptyDirIsRefusedAndKept(t *testing.T) {
	dtNeedPython(t)
	base := filepath.Join(dtRepo(t), "docs", "demo")
	d, err := os.MkdirTemp(base, "tmp") // on disk: /tmp is refused
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	keep := filepath.Join(d, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dtWant(t, dtRunTour(t, 0, "--out", d+"/o", "--demo-dir", d), 3)
	if st, err := os.Stat(keep); err != nil || !st.Mode().IsRegular() {
		t.Errorf("keep.txt was removed: %v", err)
	}
}

// ---- Integration ------------------------------------------------------------------------------------------------

func TestDemoTourIntegrationTwoPagesThreeFramesEach(t *testing.T) {
	browser := os.Getenv("DEMO_BROWSER")
	if browser == "" {
		t.Skip("set DEMO_BROWSER to a chrome-headless-shell to run")
	}
	dtNeedPython(t)
	d, err := os.MkdirTemp(filepath.Join(dtRepo(t), "docs", "demo"), "tmp") // on disk, not RAM-backed /tmp
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	out, work := filepath.Join(d, "frames"), filepath.Join(d, "work")
	r := dtRunTour(t, 300*time.Second, "--out", out, "--work", work, "--max-frames", "6", "--pages", "/,/goals",
		"--scale", "1", "--browser", browser)
	dtWant(t, r, 0)
	pngs, _ := filepath.Glob(filepath.Join(out, "*.png"))
	if len(pngs) != 6 {
		t.Fatalf("%d pngs, want 6", len(pngs))
	}
	for _, p := range pngs {
		b, err := os.ReadFile(p)
		if err != nil || len(b) < 24 {
			t.Fatalf("%s: %v", p, err)
		}
		if w, h := binary.BigEndian.Uint32(b[16:20]), binary.BigEndian.Uint32(b[20:24]); w != 1920 || h != 1080 {
			t.Errorf("%s is %dx%d, want 1920x1080", p, w, h)
		}
	}
	txts, _ := filepath.Glob(filepath.Join(work, "text", "*.txt"))
	var text strings.Builder
	for _, f := range txts {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text.Write(b)
	}
	if !strings.Contains(text.String(), "Build the checkout flow") { // seed data (acme-web goal G-002)
		t.Error("seed text missing")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.Contains(text.String(), home) {
		t.Error("text dump contains the home dir")
	}
}
