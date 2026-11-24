package tick

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
	"factory/go/internal/store"
)

// Unit remote_url_alert (_tick_u_remote_url_alert: tmp_sweep, then remote_url_alert) and unit main_protection
// (main_protection_alert, main_protection_problem), bin/factory_cli.py.

// The Python timeouts, as variables only so a test can shorten them.
var (
	remoteGitTimeout    = 30 * time.Second // git_run(..., timeout=30)
	protectionGHTimeout = 30 * time.Second // subprocess.run(["gh","api",...], timeout=30)
)

const (
	protectionEveryS     = 3600 // MAIN_PROTECTION_EVERY_S
	alertErrMax          = 300  // str(e)[:300]
	protectionStderrTail = 200  // p.stderr.strip()[-200:]
)

func remoteURLAlertUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	if err := tmpSweep(a); err != nil { // lapless: no lap row of its own, its time flows into remote_url_alert
		return err
	}
	return remoteURLAlert(a)
}

// gitOptions is the hardening of git_run: the home and the live homes.
func (a *alertEnv) gitOptions() (gitx.Options, string, error) {
	primary, homes, err := a.Env.DeriveLiveHomes()
	if err != nil {
		return gitx.Options{}, "", fmt.Errorf("derive the live homes: %w", err)
	}
	live := make([]string, 0, len(homes))
	for h := range homes {
		live = append(live, h)
	}
	sort.Strings(live)
	return gitx.Options{Home: a.Home, LiveHomes: live}, primary, nil
}

var credsInURL = "://"

// stripCreds is re.sub(r"(?<=://)[^/@\s]*@", "", u): the userinfo after every "://" goes, so a token is never copied from
// a URL into the DB. \s is Unicode whitespace.
func stripCreds(u string) string {
	var b strings.Builder
	last := 0 // end of the previous match: matches do not overlap
	for i := 0; i+3 <= len(u); i++ {
		if u[i:i+3] != credsInURL || i+3 < last {
			continue
		}
		j := i + 3
		for j < len(u) {
			r, n := utf8.DecodeRuneInString(u[j:])
			if r == '/' || r == '@' || store.PyIsSpace(r) {
				break
			}
			j += n
		}
		if j < len(u) && u[j] == '@' {
			b.WriteString(u[last : i+3])
			last = j + 1
			i = j
		}
	}
	b.WriteString(u[last:])
	return b.String()
}

// remoteURLMismatch is remote_url_mismatch: "" when factory_remote_url is empty or equals the checkout's origin URL.
func remoteURLMismatch(a *alertEnv) (string, error) {
	cfgWant, err := a.S.Cfg("factory_remote_url")
	if err != nil {
		return "", err
	}
	want := store.PyStrip(cfgWant)
	if want == "" {
		return "", nil
	}
	o, primary, err := a.gitOptions()
	if err != nil {
		return "", err
	}
	o.Timeout = remoteGitTimeout
	r, err := gitx.Run(primary, o, "remote", "get-url", "origin")
	if err != nil {
		return "", caughtErr{err}
	}
	got := ""
	if r.ExitCode == 0 {
		got = store.PyStrip(r.Stdout)
	}
	if got != "" && got == want {
		return "", nil
	}
	show := func(u string) string { return stripCreds(u) }
	shownGot := pyRepr(show(got))
	if show(got) == "" {
		shownGot = pyRepr("(none)")
	}
	return fmt.Sprintf("config factory_remote_url is %s but `git remote get-url origin` of the factory checkout is %s: the git guard will "+
		"refuse pushes. After a remote switch run `factory config factory_remote_url <new origin url>`; otherwise restore the origin.",
		pyRepr(show(want)), shownGot), nil
}

// caughtErr marks what the Python `except (OSError, SubprocessError, ValueError)` of the unit catches: a tool that cannot
// start, a timeout, a bad exit, bad JSON. Every other error (the database, an AttributeError) ends the unit, as an
// uncaught Python exception ends the tick.
type caughtErr struct{ error }

func (e caughtErr) Unwrap() error { return e.error }

func isCaught(err error) bool {
	var c caughtErr
	return errors.As(err, &c)
}

// remoteURLAlert is remote_url_alert: one alert to the director per distinct mismatch (re-armed once it matches again).
func remoteURLAlert(a *alertEnv) error {
	s := a.S
	msg, err := remoteURLMismatch(a)
	if err != nil {
		if !isCaught(err) {
			return err
		}
		return s.EventAs(pruneActor, "tick", "remote-url", "remote-url-check-failed", truncRunes(pyErrText(errors.Unwrap(err)), alertErrMax), "")
	}
	seen, _, err := configRow(s.DB, "remote_url_alerted")
	if err != nil {
		return err
	}
	if msg != "" && msg != seen {
		if _, _, err := s.Send(store.Msg{To: "director", Subject: "factory_remote_url does not match origin", Body: &msg,
			Kind: "alert", Sender: alertTickActor}); err != nil {
			return err
		}
	}
	if msg != seen {
		return setConfig(s.DB, "remote_url_alerted", msg)
	}
	return nil
}

// --- main_protection ---------------------------------------------------------------------------------------------

func mainProtectionUnit(u *UnitCtx) error {
	a, closeDB, err := openAlert(u)
	if err != nil {
		return err
	}
	defer closeDB()
	return mainProtectionAlert(a)
}

var (
	slugQueryRE  = regexp.MustCompile(`[?#].*$`)
	slugSchemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/]*/`)
	slugUserRE   = regexp.MustCompile(`^[^/@:]*@[^/:]*:`)
)

// repoSlug is _repo_slug: OWNER/NAME lower-cased without ".git", "" when there is none. A control character (a line
// break changes what Python's `.` and `$` match) reports ok=false.
func repoSlug(s string) (slug string, ok bool) {
	u := store.PyStrip(s)
	if u == "" {
		return "", true
	}
	for _, r := range u {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	u = slugQueryRE.ReplaceAllString(u, "")
	u = slugSchemeRE.ReplaceAllString(u, "")
	u = slugUserRE.ReplaceAllString(u, "")
	var parts []string
	for _, p := range strings.Split(strings.TrimRight(u, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return "", true
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if len(name) >= 4 && strings.EqualFold(name[len(name)-4:], ".git") {
		name = name[:len(name)-4]
	}
	if owner == "" || name == "" || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", true
	}
	return pyLower(owner + "/" + name), true
}

// cfgSlugs is _cfg_slugs: the set of slugs of a comma-list config key.
func cfgSlugs(s *store.Store, key string) (map[string]bool, error) {
	v, err := s.Cfg(key)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		slug, ok := repoSlug(item)
		if !ok {
			return nil, fmt.Errorf("config %s: cannot read the repo %s; fix the value or set tick_impl to python", key, pyRepr(item))
		}
		if slug != "" {
			out[slug] = true
		}
	}
	return out, nil
}

// valueErr is the ValueError class of main_protection_problem (bad JSON); the alert catches it.
type valueErr struct{ msg string }

func (e valueErr) Error() string { return e.msg }

// mainProtectionProblem is main_protection_problem: why the main branch protection of OWNER/NAME fails rule R-a, ""
// when it holds. An error means gh could not answer: unknown is never reported as fine.
func mainProtectionProblem(slug string) (string, error) {
	r, err := ghx.Run(nil, "", protectionGHTimeout, "api", "repos/"+slug+"/branches/main/protection")
	if err != nil {
		return "", caughtErr{err}
	}
	if r.ExitCode != 0 {
		if strings.Contains(strings.ToLower(r.Stdout+r.Stderr), "branch not protected") {
			return "main has no branch protection at all", nil
		}
		tail := []rune(store.PyStrip(r.Stderr))
		if len(tail) > protectionStderrTail {
			tail = tail[len(tail)-protectionStderrTail:]
		}
		return "", caughtErr{fmt.Errorf("gh api protection exited %d: %s", r.ExitCode, string(tail))}
	}
	return protectionFromJSON(r.Stdout)
}

// protectionFromJSON is the second half of main_protection_problem: the gh answer decoded and judged.
func protectionFromJSON(stdout string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	var prot any
	if err := dec.Decode(&prot); err != nil {
		return "", caughtErr{valueErr{pyJSONError(stdout, err)}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		end := int(dec.InputOffset())
		end += len(stdout[end:]) - len(strings.TrimLeft(stdout[end:], " \t\n\r"))
		return "", caughtErr{valueErr{pyJSONPos("Extra data", stdout, end)}}
	}
	obj, ok := prot.(map[string]any)
	if !ok {
		return "", fmt.Errorf("gh api protection returned a JSON %T, not an object", prot) // Python: AttributeError, not caught
	}
	var missing []string
	ea := obj["enforce_admins"]
	if pyTruthy(ea) {
		m, isObj := ea.(map[string]any)
		if !isObj {
			return "", fmt.Errorf("enforce_admins is a JSON %T, not an object", ea) // Python: AttributeError, not caught
		}
		if !pyTruthy(m["enabled"]) {
			missing = append(missing, "enforce_admins is off (admins, including the owner's login, can push to main)")
		}
	} else {
		missing = append(missing, "enforce_admins is off (admins, including the owner's login, can push to main)")
	}
	if !pyTruthy(obj["required_pull_request_reviews"]) {
		missing = append(missing, "pull requests are not required")
	}
	return strings.Join(missing, "; "), nil
}

// pyJSONError approximates json.JSONDecodeError's text. Python reports "Expecting value" at the start of the value
// when that cannot start any JSON value (the usual garbage), which is exact here; inside an object or array it uses the
// byte Go stopped at.
func pyJSONError(s string, err error) string {
	pos := len(s) - len(strings.TrimLeft(s, " \t\n\r"))
	var se *json.SyntaxError
	if pos < len(s) && (s[pos] == '{' || s[pos] == '[') && errors.As(err, &se) {
		pos = int(se.Offset) - 1
		if pos < 0 {
			pos = 0
		}
	}
	return pyJSONPos("Expecting value", s, pos)
}

// pyJSONPos is the "<msg>: line L column C (char N)" text of json.JSONDecodeError for byte offset pos of s (Python counts
// characters; the stdout of gh is ASCII JSON in practice).
func pyJSONPos(msg, s string, pos int) string {
	if pos > len(s) {
		pos = len(s)
	}
	line := 1 + strings.Count(s[:pos], "\n")
	col := pos + 1
	if i := strings.LastIndex(s[:pos], "\n"); i >= 0 {
		col = pos - i
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, col, pos)
}

// mainProtectionAlert is main_protection_alert (R-a): once an hour check the main protection of every
// public_text_repos repo that is not frozen; alert the boss for each one that lacks enforce_admins or the PR requirement.
func mainProtectionAlert(a *alertEnv) error {
	s := a.S
	pub, err := cfgSlugs(s, "public_text_repos")
	if err != nil {
		return err
	}
	frozen, err := cfgSlugs(s, "frozen_repos")
	if err != nil {
		return err
	}
	var slugs []string
	for k := range pub {
		if !frozen[k] {
			slugs = append(slugs, k)
		}
	}
	if len(slugs) == 0 {
		return nil
	}
	sort.Strings(slugs)
	checked, has, err := configRow(s.DB, "main_protection_checked_at")
	if err != nil {
		return err
	}
	if has {
		prev, ok1 := pyFromISO(checked, nil)
		cur, ok2 := pyFromISO(s.Now(), nil)
		if ok1 && ok2 && epochSeconds(cur)-epochSeconds(prev) < protectionEveryS {
			return nil
		} // an unreadable stamp: check now and overwrite it
	}
	if err := setConfig(s.DB, "main_protection_checked_at", s.Now()); err != nil {
		return err
	}
	for _, slug := range slugs {
		why, err := mainProtectionProblem(slug)
		if err != nil {
			if !isCaught(err) {
				return err
			}
			if eerr := s.EventAs(pruneActor, "tick", "main-protection", "main-protection-check-failed",
				slug+": "+truncRunes(pyErrText(errors.Unwrap(err)), alertErrMax), ""); eerr != nil {
				return eerr
			}
			continue
		}
		if why != "" {
			body := fmt.Sprintf("%s main: %s. Owner rule R-a: PR required (0 approvals), enforce_admins on, no force push, no deletion. "+
				"The owner fixes it with `python3 ci/publish_repo.py --repo %s --apply`; agents may not change protection. "+
				"Next reminder in one hour while it stays wrong.", slug, why, slug)
			if _, _, err := s.Send(store.Msg{To: "boss", Subject: fmt.Sprintf("public repo %s: main protection is too weak", slug),
				Body: &body, Kind: "alert", Sender: alertTickActor}); err != nil {
				return err
			}
		}
	}
	return nil
}

// pyLower is str.lower(): Go's ToLower differs for U+0130 (Python: "i" + U+0307) and for a capital sigma at the end of a
// word (Python: final sigma U+03C2).
func pyLower(s string) string {
	rs := []rune(s)
	var b strings.Builder
	cased := func(r rune) bool {
		return unicode.IsLetter(r) && (unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r))
	}
	for i, r := range rs {
		switch {
		case r == 0x130:
			b.WriteString("i\u0307")
		case r == 0x3a3 && i > 0 && cased(rs[i-1]) && (i+1 >= len(rs) || !cased(rs[i+1])):
			b.WriteRune(0x3c2)
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
