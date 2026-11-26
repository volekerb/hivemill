package hm

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tokens of docs/design/obs-ui.md, each set in the light and the dark theme.
var obsDocTokens = []string{"--bg", "--panel", "--head", "--fg", "--muted", "--line", "--edge",
	"--honey", "--honey-fg", "--ok", "--warn", "--bad", "--s0", "--s1", "--s2"}

const obsCSP = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// obsShellPages are the pages the shell tests walk: the three top sections, a drill-down and a not-found page.
var obsShellPages = []string{"/", "/spend", "/flow", "/project?p=demo-app", "/nope"}

// obsFixtureHandler serves testdata/obs_fixture: box-a's log and the log pulled from box-b, with the dates moved so
// the newest day is yesterday and the fixture falls in the page's window (as ci/obs_screens.sh does).
func obsFixtureHandler(t *testing.T) http.Handler {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Log.Machine = "box-a"
	day := regexp.MustCompile(`("ts":")(\d{4}-\d\d-\d\d)`)
	texts := map[string]string{}
	var newest time.Time
	for _, name := range []string{"events.jsonl", filepath.Join("box-b", "events.jsonl")} {
		b, err := os.ReadFile(filepath.Join("testdata", "obs_fixture", name))
		if err != nil {
			t.Fatal(err)
		}
		texts[name] = string(b)
		for _, m := range day.FindAllStringSubmatch(string(b), -1) {
			if d, _ := time.Parse("2006-01-02", m[2]); d.After(newest) {
				newest = d
			}
		}
	}
	shift := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Sub(newest)
	dst := map[string]string{"events.jsonl": r.Log.Path, filepath.Join("box-b", "events.jsonl"): filepath.Join(r.Env.ObsDir(), "box-b", "events.jsonl")}
	for name, text := range texts {
		moved := day.ReplaceAllStringFunc(text, func(s string) string {
			m := day.FindStringSubmatch(s)
			d, _ := time.Parse("2006-01-02", m[2])
			return m[1] + d.Add(shift).Format("2006-01-02")
		})
		if err := os.MkdirAll(filepath.Dir(dst[name]), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst[name], []byte(moved), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// obsThemes parses the stylesheet's light set (the first :root block) and its forced dark set ([data-theme="dark"]),
// and checks the system dark set (inside prefers-color-scheme: dark) equals the forced one.
func obsThemes(t *testing.T, css string) (light, dark map[string]string) {
	t.Helper()
	decl := regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*(#[0-9a-fA-F]{6})`)
	block := func(re string) map[string]string {
		m := regexp.MustCompile(re).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("stylesheet has no block %s", re)
		}
		set := map[string]string{}
		for _, d := range decl.FindAllStringSubmatch(m[1], -1) {
			set[d[1]] = strings.ToLower(d[2])
		}
		return set
	}
	light = block(`(?s):root\s*\{([^}]*)\}`)
	dark = block(`(?s):root\[data-theme="dark"\]\s*\{([^}]*)\}`)
	system := block(`(?s)@media \(prefers-color-scheme: dark\)\s*\{\s*:root:not\(\[data-theme="light"\]\)\s*\{([^}]*)\}`)
	for k, v := range dark {
		if system[k] != v {
			t.Errorf("system dark %s = %q, forced dark %q", k, system[k], v)
		}
	}
	return light, dark
}

func TestObsShellTokensBothThemes(t *testing.T) {
	h := obsFixtureHandler(t)
	_, css := obsGet(t, h, "/assets/obs/app.css")
	light, dark := obsThemes(t, css)
	for _, tok := range obsDocTokens {
		if light[tok] == "" || dark[tok] == "" {
			t.Errorf("token %s: light %q, dark %q", tok, light[tok], dark[tok])
		}
	}
	if light["--s0"] != light["--honey"] || dark["--s0"] != dark["--honey"] {
		t.Error("--s0 is --honey in both themes")
	}
	sizes := regexp.MustCompile(`font-size\s*:\s*([0-9.]+)(rem|px|em)?`).FindAllStringSubmatch(css, -1)
	if len(sizes) == 0 {
		t.Fatal("no font-size in the stylesheet")
	}
	for _, m := range sizes {
		v, _ := strconv.ParseFloat(m[1], 64)
		if m[2] == "px" && v < 12 || m[2] != "px" && v < 0.75 {
			t.Errorf("font-size %s%s is below 0.75rem / 12px", m[1], m[2])
		}
	}
	if regexp.MustCompile(`font\s*:\s*[0-9]`).MatchString(css) {
		t.Error("a font shorthand sets a size; use font-size")
	}
	if !strings.Contains(css, "prefers-reduced-motion: no-preference") || strings.Count(css, "transition:") != 1 {
		t.Error("one transition, inside prefers-reduced-motion: no-preference")
	}
	if !strings.Contains(css, "outline:2px solid var(--honey)") || !strings.Contains(css, "min-height:24px") {
		t.Error("2px honey focus outline and 24px targets")
	}
}

// obsLum is the WCAG relative luminance of a #rrggbb colour.
func obsLum(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 {
		t.Fatalf("colour %q", hex)
	}
	l := 0.0
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		n, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("colour %q", hex)
		}
		c := float64(n) / 255
		if c <= 0.04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		l += w * c
	}
	return l
}

func obsContrast(t *testing.T, a, b string) float64 {
	x, y := obsLum(t, a), obsLum(t, b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

func TestObsShellContrastAA(t *testing.T) {
	h := obsFixtureHandler(t)
	_, css := obsGet(t, h, "/assets/obs/app.css")
	light, dark := obsThemes(t, css)
	for name, set := range map[string]map[string]string{"light": light, "dark": dark} {
		for _, fg := range []string{"--fg", "--muted", "--honey-fg", "--ok", "--warn", "--bad"} {
			for _, bg := range []string{"--bg", "--panel", "--head"} {
				if r := obsContrast(t, set[fg], set[bg]); r < 4.5 {
					t.Errorf("%s: %s on %s is %.2f:1, below 4.5", name, fg, bg, r)
				}
			}
		}
		if r := obsContrast(t, set["--edge"], set["--bg"]); r < 3 {
			t.Errorf("%s: --edge on --bg is %.2f:1, below 3", name, r)
		}
	}
}

func TestObsShellNavCurrentAndSkipLink(t *testing.T) {
	h := obsFixtureHandler(t)
	current := regexp.MustCompile(`<a [^>]*aria-current="page"[^>]*>([^<]*)</a>`)
	rendered := regexp.MustCompile(`rendered \d{4}-\d\d-\d\d \d\d:\d\d`)
	own := map[string]string{"/": "Now", "/spend": "Spend", "/flow": "Flow"}
	for _, path := range obsShellPages {
		code, body := obsGet(t, h, path)
		if want := own[path]; want != "" {
			got := current.FindAllStringSubmatch(body, -1)
			if code != 200 || len(got) != 1 || got[0][1] != want || strings.Count(body, `aria-current="page"`) != 1 {
				t.Errorf("%s: %d, aria-current on %v, want only %s", path, code, got, want)
			}
			if strings.Contains(body, `class="crumbs"`) {
				t.Errorf("%s: a top section has no breadcrumb", path)
			}
		} else if strings.Contains(body, "aria-current") {
			t.Errorf("%s: a drill-down or not-found page has no current entry", path)
		}
		if !strings.Contains(body, `<a class="skip" href="#main">`) || !strings.Contains(body, `<main id="main"`) {
			t.Errorf("%s: no skip link to main", path)
		}
		if !rendered.MatchString(body) {
			t.Errorf("%s: no render time in the header", path)
		}
		if strings.Contains(body, "loopback") {
			t.Errorf("%s: the footer still says loopback", path)
		}
	}
	code, body := obsGet(t, h, "/nope")
	if code != 404 || !strings.Contains(body, `<aside class="side">`) || !strings.Contains(body, `<p class="bad notfound">`) ||
		!strings.Contains(body, ">Back to Now</a>") {
		t.Errorf("/nope: %d, want the full shell with a --bad message and a link to Now\n%s", code, body)
	}
}

func TestObsShellReadOnlyNoScript(t *testing.T) {
	h := obsFixtureHandler(t)
	for _, path := range obsShellPages {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "127.0.0.1:7461"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != 200 && w.Code != 404 || !strings.Contains(body, "<!doctype html>") {
			t.Errorf("GET %s: %d", path, w.Code)
		}
		if strings.Contains(strings.ToLower(body), "<script") || regexp.MustCompile(`\sstyle=`).MatchString(body) {
			t.Errorf("%s: a script tag or a style attribute", path)
		}
		if got := w.Header().Get("Content-Security-Policy"); got != obsCSP {
			t.Errorf("%s: CSP %q", path, got)
		}
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			req := httptest.NewRequest(method, path, strings.NewReader("x=1"))
			req.Host = "127.0.0.1:7461"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d, want 405", method, path, w.Code)
			}
		}
	}
}
