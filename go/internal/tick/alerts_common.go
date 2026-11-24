package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/gitx"
	"factory/go/internal/identity"
	"factory/go/internal/store"
)

// Shared pieces of the hygiene alert units (digest, tmp_alert, tmp_sweep + remote_url_alert, main_protection_alert of
// bin/factory_cli.py). Each unit works on its own *store.Store; the stored timestamps come from the S2 events clock
// (FACTORY_TEST_NOW on a non-live home), the wall clock of the checks (dt.datetime.now, time.time) is alertWall.

// alertWall is the real wall clock Python reads with dt.datetime.now(), dt.date.today() and time.time(). Tests replace it.
var alertWall = time.Now

// alertTickActor is the sender of every alert message ("tick").
const alertTickActor = "tick"

// alertEnv is what one alert unit works on.
type alertEnv struct {
	S    *store.Store
	Env  identity.Env
	Home string
}

// openAlert opens the unit's database and store. The returned func closes the database.
func openAlert(u *UnitCtx) (*alertEnv, func(), error) {
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open the factory database: %w", err)
	}
	env := pruneIdentity()
	clock, err := pruneEventClock(env)
	if err != nil {
		closeDB()
		return nil, func() {}, err
	}
	return &alertEnv{S: &store.Store{DB: db, Clock: clock}, Env: env, Home: u.Home}, closeDB, nil
}

// setConfig is _set_config: upsert one config row.
func setConfig(db *sql.DB, key, value string) error {
	if _, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
		return fmt.Errorf("write config %s: %w", key, err)
	}
	return nil
}

// configRow is the config value of key and whether the row exists (a NULL value reads as "").
func configRow(db *sql.DB, key string) (string, bool, error) {
	var v sql.NullString
	err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v)
	switch {
	case err == nil:
		return v.String, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	}
	return "", false, fmt.Errorf("read config %s: %w", key, err)
}

// pyFloat is float(s): optional Unicode whitespace around an ASCII float literal (inf, nan and underscores between
// digits as Python reads them). ok=false is Python's ValueError.
func pyFloat(s string) (float64, bool) {
	t := store.PyStrip(s)
	if t == "" {
		return 0, false
	}
	t = asciiDigits(t)
	for _, r := range t {
		if r > 0x7f {
			return 0, false
		}
	}
	if strings.Contains(t, "_") {
		if strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") ||
			strings.Contains(t, "_.") || strings.Contains(t, "._") || strings.Contains(t, "e_") || strings.Contains(t, "_e") {
			return 0, false
		}
		t = strings.ReplaceAll(t, "_", "")
	}
	low := strings.ToLower(strings.TrimLeft(t, "+-"))
	if strings.HasPrefix(low, "0x") || strings.HasPrefix(low, "0b") || strings.HasPrefix(low, "0o") {
		return 0, false // Go's ParseFloat reads hex floats, Python does not
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return f, true // Python float("1e999") is inf, not an error
		}
		return 0, false
	}
	return f, true
}

// cfgFloat is float(cfg(con, key)); a bad value is the ValueError text Python prints.
func cfgFloat(s *store.Store, key string) (float64, error) {
	v, err := s.Cfg(key)
	if err != nil {
		return 0, err
	}
	f, ok := pyFloat(v)
	if !ok {
		return 0, fmt.Errorf("could not convert string to float: %s", pyRepr(v))
	}
	return f, nil
}

// pyRepr is repr(str).
func pyRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			fmt.Fprintf(&b, "\\x%02x", s[i]) // not valid UTF-8: Python never has such a str (it would be surrogates)
			i++
			continue
		}
		i += n
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(q):
			b.WriteString(`\` + string(q))
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == ' ' || (r > 0x20 && r < 0x7f):
			b.WriteRune(r)
		case r < 0x7f && r >= 0:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == 0x7f:
			b.WriteString(`\x7f`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// truncRunes is already in prune.go: Python s[:n].

// pyErrText is str(e) of the OSError or SubprocessError a Python call would raise for err.
func pyErrText(err error) string {
	var te *gitx.TimeoutError
	if errors.As(err, &te) {
		return te.Error()
	}
	var ee *exec.Error
	name := ""
	inner := err
	if errors.As(err, &ee) {
		name, inner = ee.Name, ee.Err
	}
	var pe *fs.PathError
	if errors.As(inner, &pe) {
		if name == "" {
			name = pe.Path
		}
		inner = pe.Err
	}
	if errors.Is(inner, exec.ErrNotFound) {
		inner = syscall.ENOENT
	}
	var en syscall.Errno
	if errors.As(inner, &en) && name != "" {
		msg := en.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		return fmt.Sprintf("[Errno %d] %s: %s", int(en), msg, pyRepr(name))
	}
	return err.Error()
}

// pyRound1 is round(x, 1): correctly rounded on the exact binary value, ties to even (what FormatFloat does).
func pyRound1(x float64) float64 {
	f, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	if err != nil {
		return x
	}
	return f
}

// pyRealpath is os.path.realpath(p) (not strict): every symlink that exists is resolved, a component that does not
// exist is kept as written, a loop stops after 40 links and keeps the rest unresolved.
func pyRealpath(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = filepath.Join(wd, p)
		}
	}
	rest := strings.Split(p, "/")
	resolved := "/"
	links := 0
	for len(rest) > 0 {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		links++
		if links > 40 {
			resolved = next
			continue
		}
		tgt, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		if strings.HasPrefix(tgt, "/") {
			resolved = "/"
		}
		rest = append(strings.Split(tgt, "/"), rest...)
	}
	return resolved
}

// alertNow is the instant of alertWall as epoch seconds (time.time()).
func alertNow() float64 { return epochSeconds(alertWall()) }

// asciiDigits maps every Unicode decimal digit to its ASCII digit, as Python's float() and int() read them. The digits of a
// script are consecutive code points starting at a zero, so the value is the offset in the run of Nd characters mod 10.
func asciiDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0x7f && unicode.Is(unicode.Nd, r) {
			z := r
			for unicode.Is(unicode.Nd, z-1) {
				z--
			}
			r = '0' + (r-z)%10
		}
		b.WriteRune(r)
	}
	return b.String()
}
