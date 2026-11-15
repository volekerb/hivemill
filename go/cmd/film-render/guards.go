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

// inlineTags join their neighbours without a visual break, so text split by them reads as one word.
var inlineTags = map[string]bool{
	"b": true, "i": true, "em": true, "strong": true, "span": true, "a": true, "code": true,
	"small": true, "sup": true, "sub": true, "mark": true, "u": true, "s": true,
	"abbr": true, "font": true, "kbd": true, "q": true, "cite": true, "time": true, "label": true, "var": true,
	"samp": true, "del": true, "ins": true, "bdi": true, "bdo": true, "data": true, "big": true, "tt": true,
	"wbr": true, "dfn": true, "strike": true, "nobr": true,
}

// pageScan is what a scene page shows: text with every tag a space, text joined across inline tags, and the
// string literals of CSS content: declarations (style blocks and style attributes).
type pageScan struct {
	text, joined string
	css          []string
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// scanTag parses the tag starting at s[i] == '<' (s[i+1] is a letter or '/'). It returns the lower-case name, whether
// it is a closing tag, the style attribute values, and the index after the tag. Quoted attribute values may hold '>'.
// An unterminated tag runs to the end of the input.
func scanTag(s string, i int) (name string, closing bool, styles []string, end int) {
	j := i + 1
	if s[j] == '/' {
		closing = true
		j++
	}
	k := j
	for k < len(s) && !strings.ContainsRune(" \t\r\n\f/>", rune(s[k])) {
		k++
	}
	name = strings.ToLower(s[j:k])
	j = k
	for j < len(s) {
		for j < len(s) && strings.ContainsRune(" \t\r\n\f/", rune(s[j])) {
			j++
		}
		if j >= len(s) {
			break
		}
		if s[j] == '>' {
			return name, closing, styles, j + 1
		}
		k = j
		for k < len(s) && !strings.ContainsRune(" \t\r\n\f/=>", rune(s[k])) {
			k++
		}
		if k == j { // a stray '=' starts a name
			k++
		}
		attr := strings.ToLower(s[j:k])
		j = k
		for j < len(s) && strings.ContainsRune(" \t\r\n\f", rune(s[j])) {
			j++
		}
		if j >= len(s) || s[j] != '=' {
			continue
		}
		j++
		for j < len(s) && strings.ContainsRune(" \t\r\n\f", rune(s[j])) {
			j++
		}
		var val string
		switch {
		case j < len(s) && (s[j] == '"' || s[j] == '\''):
			q := strings.IndexByte(s[j+1:], s[j])
			if q < 0 {
				val, j = s[j+1:], len(s)
			} else {
				val, j = s[j+1:j+1+q], j+q+2
			}
		default:
			k = j
			for k < len(s) && !strings.ContainsRune(" \t\r\n\f>", rune(s[k])) {
				k++
			}
			val, j = s[j:k], k
		}
		if attr == "style" {
			styles = append(styles, html.UnescapeString(val))
		}
	}
	return name, closing, styles, len(s)
}

// cssContentStrings returns the string literals of every content: declaration in css.
func cssContentStrings(css string) []string {
	var out []string
	for from := 0; ; {
		i := indexFold(css[from:], "content")
		if i < 0 {
			return out
		}
		i += from
		from = i + len("content")
		if i > 0 && (isLetter(css[i-1]) || css[i-1] == '-' || css[i-1] == '_') {
			continue
		}
		j := from
		for j < len(css) && strings.ContainsRune(" \t\r\n\f", rune(css[j])) {
			j++
		}
		if j >= len(css) || css[j] != ':' {
			continue
		}
		var decl strings.Builder
		n := 0
		for j++; j < len(css) && css[j] != ';' && css[j] != '}'; {
			if css[j] != '"' && css[j] != '\'' {
				j++
				continue
			}
			q, k := css[j], j+1
			for k < len(css) && css[k] != q {
				if css[k] == '\\' {
					k++
				}
				k++
			}
			k = min(k, len(css))
			out = append(out, css[j+1:k])
			decl.WriteString(css[j+1 : k])
			n++
			j = k + 1
		}
		if n > 1 { // adjacent literals render as one string
			out = append(out, decl.String())
		}
		from = min(max(from, j), len(css))
	}
}

// scanPage walks the page left to right: tags (quoted attribute values may hold '>' or "<!--"), real comments (only in
// text position, to the first "-->", or to the end of the input) and script/style raw text (to the matching close tag)
// are not copy. CSS colours such as #123456 and script source are not copy either.
func scanPage(s string) pageScan {
	var text, joined strings.Builder
	var ps pageScan
	for i := 0; i < len(s); {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				j = len(s) - i
			}
			text.WriteString(s[i : i+j])
			joined.WriteString(s[i : i+j])
			i += j
			continue
		}
		switch {
		case strings.HasPrefix(s[i:], "<!--"):
			text.WriteByte(' ')
			switch {
			case strings.HasPrefix(s[i+4:], ">"): // "<!-->" is an empty comment
				i += 5
			case strings.HasPrefix(s[i+4:], "->"): // so is "<!--->"
				i += 6
			default:
				e, w := strings.Index(s[i+4:], "-->"), 3
				if e2 := strings.Index(s[i+4:], "--!>"); e2 >= 0 && (e < 0 || e2 < e) {
					e, w = e2, 4
				}
				if e < 0 {
					i = len(s)
				} else {
					i += 4 + e + w
				}
			}
		case i+1 < len(s) && (isLetter(s[i+1]) || s[i+1] == '/' && i+2 < len(s) && isLetter(s[i+2])):
			name, closing, styles, end := scanTag(s, i)
			for _, st := range styles {
				ps.css = append(ps.css, cssContentStrings(st)...)
			}
			text.WriteByte(' ')
			if !inlineTags[name] {
				joined.WriteByte(' ')
			}
			i = end
			if !closing && (name == "script" || name == "style") {
				e := indexFold(s[i:], "</"+name)
				body := s[i:]
				if e >= 0 {
					body = s[i : i+e]
				}
				if name == "style" {
					ps.css = append(ps.css, cssContentStrings(body)...)
				}
				if e < 0 {
					i = len(s)
				} else {
					_, _, _, i = scanTag(s, i+e)
				}
			}
		case i+1 < len(s) && (s[i+1] == '!' || s[i+1] == '?'):
			text.WriteByte(' ')
			joined.WriteByte(' ')
			e := strings.IndexByte(s[i:], '>')
			if e < 0 {
				i = len(s)
			} else {
				i += e + 1
			}
		default: // a lone '<' is text
			text.WriteByte('<')
			joined.WriteByte('<')
			i++
		}
	}
	ps.text = html.UnescapeString(text.String())
	ps.joined = html.UnescapeString(joined.String())
	return ps
}

// indexFold is strings.Index with an ASCII case-insensitive needle (lower-case ASCII; EqualFold also folds Unicode, which cannot match this needle).
func indexFold(s, needle string) int {
	n := len(needle)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], needle) {
			return i
		}
	}
	return -1
}

// htmlText is the visible text of an HTML page: script, style and comments dropped, tags a space, entities decoded.
func htmlText(s string) string { return scanPage(s).text }

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
	if sb.FPS > 120 || sb.Width > 7680 || sb.Height > 4320 {
		return refusef("guard format: width %d, height %d, fps %g exceed the limits 7680x4320 at 120 fps.", sb.Width, sb.Height, sb.FPS)
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
		ps := scanPage(string(data))
		if err := scanBanned(sc.ID, "page text", ps.text); err != nil {
			return err
		}
		if err := scanBanned(sc.ID, "page text", ps.joined); err != nil {
			return err
		}
		for _, c := range ps.css {
			if err := scanBanned(sc.ID, "page css content", c); err != nil {
				return err
			}
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
// resolves through symlinked parents. The parent is always resolved for real before a final symlink's relative
// target is joined to it, so "../" in a link target is applied to the real directory, not the link's name.
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	for i := 0; ; i++ {
		if i >= 40 {
			return "", errors.New("too many symlink levels in " + p)
		}
		dir, err := resolveDir(filepath.Dir(abs))
		if err != nil {
			return "", err
		}
		abs = filepath.Join(dir, filepath.Base(abs))
		st, lerr := os.Lstat(abs)
		if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
			return abs, nil
		}
		target, rerr := os.Readlink(abs)
		if rerr != nil {
			return "", rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		abs = filepath.Clean(target)
	}
}

// resolveDir EvalSymlinks the deepest existing ancestor of dir and appends the missing tail.
func resolveDir(dir string) (string, error) {
	rest := ""
	cur := dir
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
			return dir, nil
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

// checkOutWork is guard (e): the work dir is deleted at the end, so the film must not be written inside it.
func checkOutWork(out, work string) error {
	realOut, err := resolvePath(out)
	if err != nil {
		return failf("cannot resolve --out %s: %v", out, err)
	}
	realWork, err := resolvePath(work)
	if err != nil {
		return failf("cannot resolve --work %s: %v", work, err)
	}
	if within(realWork, realOut) {
		return refusef("guard out-work: --out %s resolves to %s, inside the work dir %s, which is deleted at the end. Write the film outside the work dir.", out, realOut, realWork)
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
