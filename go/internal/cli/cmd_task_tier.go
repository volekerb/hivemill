package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/citools/risktier"
)

// `factory task tier T [up] [T0..T3] [--classify [--base REF] [--head REF]]`: the Go port of task_tier_cmd and
// raise_task_tier in bin/factory_cli.py. The only intended difference: Python loads HOME/ci/risk_tier.py at run time
// and dies exit 2 "cannot load ci/risk_tier.py from <HOME>: <err>" when it is missing; Go has the classifier compiled
// in, so it needs ci/tier_rules.json only for --classify and dies exit 2 "cannot load ci/tier_rules.json from <HOME>:
// <err>" when that cannot be read (director note on T-1528).

func init() {
	register("task", "tier", handler{parse: parseTaskTier, run: runTaskTier})
}

// taskTierRaisers is RISK_TIER_RAISERS.
var taskTierRaisers = []string{"reviewer", "qa", "lead", "director", "boss", "judge"}

type taskTierCmd struct {
	id       string
	words    []string // text and extra, empty ones dropped (Python: `if w`)
	classify bool
	base     string
	head     string
	hasHead  bool
}

// parseTaskTier accepts one to three positionals, --classify, and --base/--head as `--flag V` or `--flag=V`.
// Anything argparse could read differently (abbreviation, other flag, repeated flag, a separate value starting with
// "-" other than "-", "--", an empty positional, a fourth positional, no id) fails the parse: Python answers.
func parseTaskTier(rest []string) (any, bool) {
	c := taskTierCmd{base: "origin/main"}
	var pos []string
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		switch {
		case w == "--classify":
			if c.classify {
				return nil, false
			}
			c.classify = true
		case strings.HasPrefix(w, "-") && w != "-":
			name, val, hasEq := strings.Cut(w, "=")
			if (name != "--base" && name != "--head") || seen[name] {
				return nil, false
			}
			if !hasEq {
				if i+1 >= len(rest) {
					return nil, false
				}
				i++
				val = rest[i]
				if strings.HasPrefix(val, "-") && val != "-" {
					return nil, false
				}
			}
			seen[name] = true
			if name == "--base" {
				c.base = val
			} else {
				c.head, c.hasHead = val, true
			}
		default:
			if w == "" || len(pos) == 3 {
				return nil, false
			}
			pos = append(pos, w)
		}
	}
	if len(pos) == 0 {
		return nil, false
	}
	c.id = pos[0]
	if len(pos) > 1 {
		c.words = pos[1:]
	}
	return c, true
}

// taskTierASCIIUpper is str.upper() for a word that can only matter if it is T0..T3: only ASCII letters change.
func taskTierASCIIUpper(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'a' && ch <= 'z' {
			b[i] = ch - 'a' + 'A'
		}
	}
	return string(b)
}

// taskTierRepr is repr() of a str as Python prints it for argv text (invalid UTF-8 bytes are the surrogate escapes
// \udcXX, as os.fsdecode gives them).
func taskTierRepr(s string) string {
	q := '\''
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteRune(q)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			fmt.Fprintf(&b, `\udc%02x`, s[i])
			i++
			continue
		}
		i += n
		switch {
		case r == q || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == ' ' || (r > 0x20 && r < 0x7f):
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
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
	b.WriteRune(q)
	return b.String()
}

func taskTierIndex(t string) int {
	for i, x := range risktier.Tiers {
		if x == t {
			return i
		}
	}
	return -1
}

func runTaskTier(c *ctx, p any) (int, error) {
	a := p.(taskTierCmd)
	var tier, branch any
	err := c.db.QueryRow("SELECT tier, branch FROM tasks WHERE id=?", a.id).Scan(&tier, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dieErr(1, "no task "+a.id)
	}
	if err != nil {
		return 0, fmt.Errorf("task tier: read task %s: %w", a.id, err)
	}
	old, err := taskTierText(tier)
	if err != nil {
		return 0, err
	}
	br, err := taskTierText(branch)
	if err != nil {
		return 0, err
	}
	words := a.words
	if len(words) > 0 && words[0] == "up" {
		words = words[1:]
	}
	if a.classify && len(words) > 0 {
		return 0, dieErr(2, "give a tier to raise to or --classify, not both")
	}
	if len(words) == 0 && !a.classify {
		if old == "" {
			old = "-"
		}
		fmt.Fprintln(c.out, old)
		return 0, nil
	}
	_, err = requireRole(c, taskTierRaisers...)
	if err != nil {
		return 0, err
	}
	var newTier, why string
	if a.classify {
		newTier, why, err = taskTierClassify(c, a, br)
		if err != nil {
			return 0, err
		}
	} else {
		newTier, why = taskTierASCIIUpper(words[0]), "raised by "+c.who
		ni := taskTierIndex(newTier)
		if ni < 0 {
			return 0, dieErr(2, "tier must be one of "+strings.Join(risktier.Tiers, ", ")+"; got "+taskTierRepr(words[0]))
		}
		if old != "" {
			oi := taskTierIndex(old)
			if oi < 0 {
				return 0, errDelegate // Python's TIERS.index raises on a stored value outside T0..T3
			}
			if ni < oi {
				return 0, dieErr(2, fmt.Sprintf("%s is %s: a tier is only ever raised, never lowered", a.id, old))
			}
		}
	}
	top := risktier.MaxTier(old, newTier)
	if top != old {
		if _, err := c.exec("UPDATE tasks SET tier=?, updated=? WHERE id=?", top, eventTS(c), a.id); err != nil {
			return 0, fmt.Errorf("task tier: store tier of %s: %w", a.id, err)
		}
		from := old
		if from == "" {
			from = "-"
		}
		if err := event(c, "task", a.id, "tier", strp(from+" -> "+top+": "+why)); err != nil {
			return 0, fmt.Errorf("task tier: record event for %s: %w", a.id, err)
		}
	}
	line := a.id + " tier " + top
	if a.classify && top != newTier {
		line += " (computed " + newTier + ", kept the higher)"
	}
	fmt.Fprintln(c.out, line)
	return 0, nil
}

// taskTierText is `v or ""` for a SQLite text column; NULL is "", anything but text goes to Python.
func taskTierText(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		if !utf8.ValidString(x) {
			return "", errDelegate
		}
		return x, nil
	}
	return "", errDelegate
}

// taskTierClassify is the --classify half: config gate, head, rules from the HOME checkout (never the PR tree),
// then the compiled-in classifier.
func taskTierClassify(c *ctx, a taskTierCmd, branch string) (string, string, error) {
	on, err := cfgText(c, "risk_tiers")
	if err != nil {
		return "", "", err
	}
	if on != "on" {
		return "", "", dieErr(2, "risk_tiers is off: the director enables it with `factory config risk_tiers on`")
	}
	head := a.head
	if !a.hasHead || head == "" {
		head = ""
		if branch != "" {
			head = "origin/" + branch
		}
	}
	if head == "" {
		return "", "", dieErr(2, a.id+" has no branch: give --head REF")
	}
	home := c.env.Home()
	rulesPath := filepath.Join(home, "ci", "tier_rules.json")
	if _, err := os.Stat(rulesPath); err != nil { // follows symlinks: a dangling link or a loop fails here
		return "", "", dieErr(2, fmt.Sprintf("cannot load ci/tier_rules.json from %s: %v", home, err))
	}
	rules, err := risktier.LoadRules(rulesPath)
	if err != nil {
		return "", "", dieErr(2, err.Error())
	}
	for _, ref := range []string{a.base, head} { // the classifier's own check quotes with %q; Python quotes with repr()
		if ref == "" || strings.HasPrefix(ref, "-") {
			return "", "", dieErr(2, "bad ref "+taskTierRepr(ref)+": a ref must not start with '-'")
		}
	}
	tier, reasons, err := risktier.ClassifyRange(home, a.base, head, rules)
	if err != nil {
		return "", "", dieErr(2, err.Error())
	}
	why := strings.Join(reasons, "; ")
	if why == "" {
		why = "no sensitive path or def"
	}
	return tier, why, nil
}
