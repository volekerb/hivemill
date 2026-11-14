package main

import (
	"encoding/json"
	"errors"
	"html"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxFilmSeconds is the hard cap on a film, whatever the storyboard says.
const maxFilmSeconds = 60

type scene struct {
	ID          string   `json:"id"`
	Start       float64  `json:"start"`
	End         float64  `json:"end"`
	Visual      string   `json:"visual"`
	Caption     string   `json:"caption"`
	Onscreen    []string `json:"onscreen"`
	Sources     []string `json:"sources"`
	Placeholder bool     `json:"placeholder"`
}

type storyboard struct {
	Title      string  `json:"title"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	MaxSeconds float64 `json:"max_seconds"`
	Scenes     []scene `json:"scenes"`
}

func loadStoryboard(path string) (*storyboard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, failf("cannot read storyboard %s: %v. Pass --storyboard FILE.", path, err)
	}
	var sb storyboard
	if err := json.Unmarshal(data, &sb); err != nil {
		return nil, failf("storyboard %s is not valid JSON for the schema: %v", path, err)
	}
	return &sb, nil
}

var bannedPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"id T-/G-/S-/M- + digits", regexp.MustCompile(`(?i)\b[tgsm]-\d+`)},
	{"id M + 3 digits", regexp.MustCompile(`(?i)\bm\d{3,}`)},
	{"# + digits", regexp.MustCompile(`#\d+`)},
	{"PR + digits", regexp.MustCompile(`(?i)\bpr ?\d+`)},
	{"SC + digit", regexp.MustCompile(`(?i)\bsc\d`)},
	{"word ticket", regexp.MustCompile(`(?i)ticket`)},
	{"word acceptance", regexp.MustCompile(`(?i)acceptance`)},
	{"version v0.5", regexp.MustCompile(`(?i)v0\.5`)},
	{"word predicted", regexp.MustCompile(`(?i)predicted`)},
}

var pr633Re = regexp.MustCompile(`PR 633\b`)

var sceneIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	commentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlText is the visible text of an HTML page: script, style and comments dropped, tags stripped, entities decoded.
// (CSS colours such as #123456 and script source are not copy.)
func htmlText(s string) string {
	s = commentRe.ReplaceAllString(s, " ")
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	return html.UnescapeString(s)
}

func scanBanned(sceneID, field, text string) error {
	for _, p := range bannedPatterns {
		if m := p.re.FindString(text); m != "" {
			return refusef("guard banned-word: scene %q %s contains %q (%s). Reword it; the film carries no ids or internal vocabulary.",
				sceneID, field, m, p.name)
		}
	}
	return nil
}

// checkTiming is guard (a): length cap, contiguous increasing scenes, a usable frame size.
func checkTiming(sb *storyboard) error {
	if sb.MaxSeconds <= 0 || sb.MaxSeconds > maxFilmSeconds {
		return refusef("guard length: max_seconds is %g; it must be > 0 and at most %d.", sb.MaxSeconds, maxFilmSeconds)
	}
	if len(sb.Scenes) == 0 {
		return refusef("guard length: the storyboard has no scenes.")
	}
	if sb.Width <= 0 || sb.Height <= 0 || sb.Width%2 != 0 || sb.Height%2 != 0 || sb.FPS <= 0 {
		return refusef("guard format: width %d, height %d, fps %g must be positive and width and height even (yuv420p).",
			sb.Width, sb.Height, sb.FPS)
	}
	var sum float64
	prevEnd := 0.0
	for i, sc := range sb.Scenes {
		if sc.End <= sc.Start {
			return refusef("guard length: scene %q ends at %g, not after its start %g.", sc.ID, sc.End, sc.Start)
		}
		if i == 0 && sc.Start < 0 {
			return refusef("guard length: scene %q starts before 0.", sc.ID)
		}
		if i > 0 && math.Abs(sc.Start-prevEnd) > 1e-6 {
			return refusef("guard length: scene %q starts at %g but the previous scene ends at %g; scenes must be contiguous.",
				sc.ID, sc.Start, prevEnd)
		}
		sum += sc.End - sc.Start
		prevEnd = sc.End
	}
	if prevEnd > sb.MaxSeconds+1e-6 || sum > sb.MaxSeconds+1e-6 {
		return refusef("guard length: film runs %g s (last scene end %g), over max_seconds %g.", sum, prevEnd, sb.MaxSeconds)
	}
	return nil
}

// checkWords is guard (b): ids and banned words in every caption, onscreen and visual string and in each scene page.
func checkWords(sb *storyboard, scenesDir string) error {
	if err := scanBanned("(title)", "title", sb.Title); err != nil {
		return err
	}
	for _, sc := range sb.Scenes {
		if !sceneIDRe.MatchString(sc.ID) {
			return refusef("guard scene-id: scene id %q must match [A-Za-z0-9_-]+.", sc.ID)
		}
		visual := sc.Visual
		if sc.Placeholder {
			visual = pr633Re.ReplaceAllString(visual, "")
		}
		if err := scanBanned(sc.ID, "caption", sc.Caption); err != nil {
			return err
		}
		for _, o := range sc.Onscreen {
			if err := scanBanned(sc.ID, "onscreen", o); err != nil {
				return err
			}
		}
		if err := scanBanned(sc.ID, "visual", visual); err != nil {
			return err
		}
		if sc.Placeholder {
			continue
		}
		page := filepath.Join(scenesDir, sc.ID+".html")
		data, err := os.ReadFile(page)
		if err != nil {
			return refusef("guard scene-file: scene %q has no readable page %s: %v.", sc.ID, page, err)
		}
		if err := scanBanned(sc.ID, "page text", htmlText(string(data))); err != nil {
			return err
		}
	}
	return nil
}

// checkPlaceholders is guard (c): final mode refuses placeholders.
func checkPlaceholders(sb *storyboard, draft bool) error {
	if draft {
		return nil
	}
	for _, sc := range sb.Scenes {
		if sc.Placeholder {
			return refusef("guard placeholder: scene %q is a placeholder and --draft was not given; final mode refuses it.", sc.ID)
		}
	}
	return nil
}

// resolvePath is Abs plus EvalSymlinks of the deepest existing ancestor, so a path that does not exist yet still
// resolves through symlinked parents.
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// A final component that is a symlink (even a dangling one) is followed by hand: EvalSymlinks cannot.
	for i := 0; ; i++ {
		st, lerr := os.Lstat(abs)
		if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
			break
		}
		if i >= 40 {
			return "", errors.New("too many symlink levels in " + p)
		}
		target, rerr := os.Readlink(abs)
		if rerr != nil {
			return "", rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		abs = filepath.Clean(target)
	}
	rest := ""
	cur := abs
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// checkOut is guard (d): the film is never written inside the repo.
func checkOut(out, repo string) error {
	realOut, err := resolvePath(out)
	if err != nil {
		return failf("cannot resolve --out %s: %v", out, err)
	}
	realRepo, err := resolvePath(repo)
	if err != nil {
		return failf("cannot resolve --repo %s: %v", repo, err)
	}
	if within(realRepo, realOut) {
		return refusef("guard out-path: --out %s resolves to %s, inside the repo %s. Write the film outside the repo.", out, realOut, realRepo)
	}
	return nil
}

func checkGuards(sb *storyboard, o *options) error {
	for _, step := range []func() error{
		func() error { return checkTiming(sb) },
		func() error { return checkWords(sb, o.scenes) },
		func() error { return checkPlaceholders(sb, o.draft) },
		func() error { return checkOut(o.out, o.repo) },
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}
