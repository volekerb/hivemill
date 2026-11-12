package cli

import (
	"database/sql"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// factory lesson add / edit / link / apply / verify / reopen (cmd_lesson write branches in bin/factory_cli.py).
// Python runs autocommit: every statement commits on its own, so the statement order below is the Python order
// (TestStmtOrderLesson). What Go does not reproduce is handed to Python before any write:
//   - a worker caller (R3: Python owns the exemption, the refusal text and its events),
//   - lesson link --pr and a lesson apply that must re-check a PR-only link (both run gh),
//   - every argv shape argparse treats differently (abbreviations, "--", unknown flags, repeated flags, a value
//     that looks like a flag, a positional after a flag, `lesson add` with no flag at all).

func init() {
	for _, a := range []string{"add", "edit", "link", "apply", "verify", "reopen"} {
		register("lesson", a, handler{parse: lessonWriteParser(a), run: runLessonWrite})
	}
}

// lessonVias is LESSON_VIAS; lessonSourceRE is LESSON_SOURCE_RE with Python's Unicode \d and \s.
var (
	lessonVias     = []string{"prompt", "settings", "hook", "skill", "cli", "other"}
	lessonSourceRE = regexp.MustCompile(`^(M\p{Nd}+|T-\p{Nd}{4}|G-\p{Nd}{3}|#\p{Nd}+|https://github\.com/[^/\s\x{0b}\x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]+/[^/\s\x{0b}\x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]+/pull/\p{Nd}+)$`)
)

// lessonWriteCmd is the parsed argparse namespace of the lesson parser (flags are shared by every action, so a flag
// that the action does not use is parsed and ignored, like Python). A nil field is an absent flag.
type lessonWriteCmd struct {
	action string
	id     *string
	flags  map[string]*string
}

var lessonFlagNames = map[string]bool{"--task": true, "--pr": true, "--via": true, "--note": true, "--evidence": true,
	"--reason": true, "--title": true, "--roles": true, "--rule": true, "--what": true, "--source": true,
	"--status": true, "--role": true}

func lessonWriteParser(action string) func([]string) (any, bool) {
	return func(rest []string) (any, bool) {
		for _, x := range rest {
			if !utf8.ValidString(x) {
				return nil, false // Python holds surrogate-escaped text and fails differently
			}
		}
		c := lessonWriteCmd{action: action, flags: map[string]*string{}}
		i := 0
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			if rest[0] == "" {
				return nil, false
			}
			c.id, i = strp(rest[0]), 1
		}
		for ; i < len(rest); i++ {
			name, val, hasVal := strings.Cut(rest[i], "=")
			if !lessonFlagNames[name] || c.flags[name] != nil {
				return nil, false
			}
			if !hasVal {
				if i+1 >= len(rest) {
					return nil, false
				}
				i++
				val = rest[i]
				if val != "-" && strings.HasPrefix(val, "-") {
					return nil, false
				}
			}
			c.flags[name] = strp(val)
		}
		if action == "add" && len(c.flags) == 0 && c.id == nil {
			return nil, false // the bare usage form stays with Python
		}
		if action == "link" && c.flags["--pr"] != nil {
			return nil, false // gh
		}
		return c, true
	}
}

func (c lessonWriteCmd) flag(n string) *string { return c.flags[n] }

// truthy is Python truthiness of an optional string.
func lwTruthy(s *string) bool { return s != nil && *s != "" }

func lwStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// lwRead is read_text(val). A relative @path is made absolute against the Deps working directory (the process cwd
// in production), so the file is found where Python finds it; no cleaning, so ".." keeps its OS meaning.
func lwRead(c *ctx, val *string) (*string, error) {
	if val != nil && strings.HasPrefix(*val, "@") && len(*val) > 1 && !strings.HasPrefix(*val, "@/") {
		cwd, err := c.deps.getwd()
		if err != nil {
			return nil, errDelegate
		}
		abs := strings.TrimRight(cwd, "/") + "/" + (*val)[1:]
		if _, err := os.Stat(pyPathNorm(abs)); err != nil {
			return val, nil // no such file: Python keeps the literal "@name"
		}
		val = strp("@" + abs)
	}
	return readText(c, val, false)
}

// lwRoles is _lesson_roles.
func lwRoles(val *string) ([]string, error) {
	var roles []string
	for _, r := range strings.Split(lwStr(val), ",") {
		if r = pyStrip(r); r != "" {
			roles = append(roles, r)
		}
	}
	valid := strings.Join(append(append([]string{}, lessonRoles...), "all"), ", ")
	if len(roles) == 0 {
		return nil, dieErr(2, "--roles is empty; give a comma list of: "+valid)
	}
	var bad []string
	for _, r := range roles {
		if r != "all" && !inList(lessonRoles, r) {
			bad = append(bad, r)
		}
	}
	if len(bad) > 0 {
		return nil, dieErr(2, "unknown role(s) "+strings.Join(bad, ",")+"; valid: "+valid)
	}
	return lwDedupe(roles), nil
}

// lwSources is _lesson_sources.
func lwSources(val *string) ([]string, error) {
	var toks []string
	for _, t := range strings.Split(lwStr(val), ",") {
		if t = pyStrip(t); t != "" {
			toks = append(toks, t)
		}
	}
	for _, t := range toks {
		if !lessonSourceRE.MatchString(t) {
			return nil, dieErr(2, "bad source '"+t+"'; use M<n>, T-<nnnn>, G-<nnn>, #<n> or a github PR url")
		}
	}
	return toks, nil
}

// lwDedupe is list(dict.fromkeys(l)).
func lwDedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// lwSplit is [t for t in s.split(",") if t].
func lwSplit(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

type lessonRow struct {
	id, title, roles, rule, sources, status string
	what                                    sql.NullString
	recurrences                             int64
}

// lwRow is _lesson_row.
func lwRow(c *ctx, id *string, usage string) (*lessonRow, error) {
	if !lwTruthy(id) {
		return nil, dieErr(2, "usage: "+usage)
	}
	var r lessonRow
	var title, roles, rule, sources, status sql.NullString
	err := c.db.QueryRow("SELECT id,title,roles,what,rule,sources,status,recurrences FROM lessons WHERE id=?", *id).
		Scan(&r.id, &title, &roles, &r.what, &rule, &sources, &status, &r.recurrences)
	if err == sql.ErrNoRows {
		return nil, dieErr(2, "no lesson "+*id)
	}
	if err != nil {
		return nil, errDelegate
	}
	if !title.Valid || !roles.Valid || !rule.Valid || !sources.Valid || !status.Valid {
		return nil, errDelegate // Python would crash on None
	}
	r.title, r.roles, r.rule, r.sources, r.status = title.String, roles.String, rule.String, sources.String, status.String
	return &r, nil
}

func runLessonWrite(c *ctx, p any) (int, error) {
	a := p.(lessonWriteCmd)
	if role, del := callerIsWorker(c); del || role != "" {
		return 0, errDelegate
	}
	var err error
	if a.action == "verify" {
		_, err = requireRole(c, "boss", "director")
	} else {
		_, err = requireRole(c, "boss", "director", "lead")
	}
	if err != nil {
		return 0, err
	}
	switch a.action {
	case "add":
		return lessonAdd(c, a)
	case "edit":
		return lessonEdit(c, a)
	case "link":
		return lessonLink(c, a)
	case "apply":
		return lessonApply(c, a)
	case "verify":
		return lessonVerify(c, a)
	}
	return lessonReopen(c, a)
}

func lessonAdd(c *ctx, a lessonWriteCmd) (int, error) {
	if a.id != nil {
		return 0, dieErr(2, "add takes no id (ids are assigned): factory lesson add --title ... --roles ... --rule ...")
	}
	title := pyStrip(lwStr(a.flag("--title")))
	if title == "" {
		return 0, dieErr(2, "--title is required and may not be empty")
	}
	roles, err := lwRoles(a.flag("--roles"))
	if err != nil {
		return 0, err
	}
	sources, err := lwSources(a.flag("--source"))
	if err != nil {
		return 0, err
	}
	rule, err := lwRead(c, a.flag("--rule"))
	if err != nil {
		return 0, err
	}
	what, err := lwRead(c, a.flag("--what"))
	if err != nil {
		return 0, err
	}
	if pyStrip(lwStr(rule)) == "" {
		return 0, dieErr(2, "--rule is required and may not be empty")
	}
	lid, err := nextID(c, "lessons", "L", 3)
	if err != nil {
		return 0, err
	}
	ts := eventTS(c)
	var whatV any
	if w := pyStrip(lwStr(what)); w != "" {
		whatV = w
	}
	if _, err := c.exec("INSERT INTO lessons(id,title,roles,what,rule,sources,status,author,created,updated) "+
		"VALUES(?,?,?,?,?,?,'open',?,?,?)",
		lid, title, strings.Join(roles, ","), whatV, pyStrip(*rule), strings.Join(lwDedupe(sources), ","), c.who, ts, ts); err != nil {
		return 0, err
	}
	if err := event(c, "lesson", lid, "add", strp(title)); err != nil {
		return 0, err
	}
	c.out.WriteString(lid + "\n")
	return 0, nil
}

func lessonEdit(c *ctx, a lessonWriteCmd) (int, error) {
	if !lwTruthy(a.id) {
		return 0, dieErr(2, "usage: factory lesson edit L-xxx [--title] [--roles] [--rule] [--what] [--source]")
	}
	row, err := lwRow(c, a.id, "")
	if err != nil {
		return 0, err
	}
	var keys []string
	vals := map[string]any{}
	set := func(k string, v any) {
		if _, ok := vals[k]; !ok {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	if t := a.flag("--title"); t != nil {
		if pyStrip(*t) == "" {
			return 0, dieErr(2, "--title may not be empty")
		}
		set("title", pyStrip(*t))
	}
	if r := a.flag("--roles"); r != nil {
		roles, err := lwRoles(r)
		if err != nil {
			return 0, err
		}
		set("roles", strings.Join(roles, ","))
	}
	var newSrc []string
	if s := a.flag("--source"); s != nil {
		if newSrc, err = lwSources(s); err != nil {
			return 0, err
		}
	}
	rule, err := lwRead(c, a.flag("--rule"))
	if err != nil {
		return 0, err
	}
	what, err := lwRead(c, a.flag("--what"))
	if err != nil {
		return 0, err
	}
	if rule != nil {
		if pyStrip(*rule) == "" {
			return 0, dieErr(2, "--rule may not be empty")
		}
		set("rule", pyStrip(*rule))
	}
	if what != nil {
		if w := pyStrip(*what); w != "" {
			set("what", w)
		} else {
			set("what", nil)
		}
	}
	if len(newSrc) > 0 {
		have := lwSplit(row.sources)
		merged := lwDedupe(append(append([]string{}, have...), newSrc...))
		if strings.Join(merged, "\x00") != strings.Join(have, "\x00") {
			set("sources", strings.Join(merged, ","))
		}
	}
	cur := map[string]any{"title": row.title, "roles": row.roles, "rule": row.rule, "sources": row.sources}
	if row.what.Valid {
		cur["what"] = row.what.String
	} else {
		cur["what"] = nil
	}
	var upd []string
	var args []any
	for _, k := range keys {
		if vals[k] != cur[k] {
			upd = append(upd, k)
			args = append(args, vals[k])
		}
	}
	if len(upd) == 0 {
		return 0, dieErr(2, "nothing to change")
	}
	sets := make([]string, len(upd))
	for i, k := range upd {
		sets[i] = k + "=?"
	}
	if _, err := c.exec("UPDATE lessons SET "+strings.Join(sets, ",")+",updated=? WHERE id=?",
		append(args, eventTS(c), *a.id)...); err != nil {
		return 0, err
	}
	names := strings.Join(upd, ",")
	if err := event(c, "lesson", *a.id, "edit", strp(names)); err != nil {
		return 0, err
	}
	c.out.WriteString(*a.id + " updated: " + names + "\n")
	return 0, nil
}

func lessonLink(c *ctx, a lessonWriteCmd) (int, error) {
	usage := "factory lesson link L-xxx (--task T-xxxx | --pr URL) [--via prompt|settings|hook|skill|cli|other]"
	if _, err := lwRow(c, a.id, usage); err != nil {
		return 0, err
	}
	task, via := a.flag("--task"), a.flag("--via")
	if lwTruthy(task) == lwTruthy(a.flag("--pr")) { // parse delegates any --pr, so only --task can be set here
		return 0, dieErr(2, "give exactly one of --task and --pr: "+usage)
	}
	if lwTruthy(via) && !inList(lessonVias, *via) {
		return 0, dieErr(2, "--via must be one of "+strings.Join(lessonVias, ", "))
	}
	var tStatus string
	var tPR sql.NullString
	err := c.db.QueryRow("SELECT status, pr_url FROM tasks WHERE id=?", *task).Scan(&tStatus, &tPR)
	if err == sql.ErrNoRows {
		return 0, dieErr(2, "no task "+*task)
	}
	if err != nil {
		return 0, errDelegate
	}
	var one int
	switch err := c.db.QueryRow("SELECT 1 FROM lesson_links WHERE lesson_id=? AND task_id=?", *a.id, *task).Scan(&one); err {
	case nil:
		return 0, dieErr(2, *a.id+" is already linked to "+*task)
	case sql.ErrNoRows:
	default:
		return 0, errDelegate
	}
	var merged, prURL any
	var vCreated sql.NullString
	switch err := c.db.QueryRow("SELECT created FROM verdicts WHERE task_id=? AND role='judge' AND verdict='merge' ORDER BY id DESC LIMIT 1", *task).Scan(&vCreated); err {
	case nil:
		if tStatus == "done" {
			merged = lwNullAny(vCreated)
			prURL = lwNullAny(tPR)
		}
	case sql.ErrNoRows:
	default:
		return 0, errDelegate
	}
	var viaV any
	if via != nil {
		viaV = *via
	}
	if _, err := c.exec("INSERT INTO lesson_links(lesson_id,task_id,pr_url,via,merged_at,author,created) VALUES(?,?,?,?,?,?,?)",
		*a.id, *task, prURL, viaV, merged, c.who, eventTS(c)); err != nil {
		return 0, err
	}
	// Python: `merged` is the verdict's created text; a NULL one is falsy, so the link reads as unmerged.
	suffix, out := " (unmerged)", " (not merged yet)"
	if merged != nil && merged != "" {
		suffix, out = " (merged)", " (merged)"
	}
	if err := event(c, "lesson", *a.id, "link", strp(*task+suffix)); err != nil {
		return 0, err
	}
	c.out.WriteString(*a.id + " linked to " + *task + out + "\n")
	return 0, nil
}

func lwNullAny(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

func lessonApply(c *ctx, a lessonWriteCmd) (int, error) {
	r, err := lwRow(c, a.id, "factory lesson apply L-xxx [--note TEXT]")
	if err != nil {
		return 0, err
	}
	if r.status != "open" {
		return 0, dieErr(2, *a.id+" is "+r.status+", only an open lesson can be applied")
	}
	var one int
	switch err := c.db.QueryRow("SELECT 1 FROM lesson_links WHERE lesson_id=?", *a.id).Scan(&one); err {
	case nil:
	case sql.ErrNoRows:
		return 0, dieErr(2, *a.id+" has no links; link the task or PR that applied it first")
	default:
		return 0, errDelegate
	}
	type link struct {
		id       int64
		task, pr sql.NullString
	}
	rows, err := c.query("SELECT id, task_id, pr_url FROM lesson_links WHERE lesson_id=? AND merged_at IS NULL ORDER BY id", *a.id)
	if err != nil {
		return 0, errDelegate
	}
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.task, &l.pr); err != nil {
			rows.Close()
			return 0, errDelegate
		}
		links = append(links, l)
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, errDelegate
	}
	for _, l := range links {
		if !l.task.Valid || l.task.String == "" {
			return 0, errDelegate // a PR-only link is re-checked through gh
		}
	}
	var left []string
	for _, l := range links {
		var created, vpr sql.NullString
		switch err := c.db.QueryRow("SELECT created, pr_url FROM verdicts WHERE task_id=? AND role='judge' AND verdict='merge' ORDER BY id DESC LIMIT 1", l.task.String).Scan(&created, &vpr); err {
		case nil:
		case sql.ErrNoRows:
			left = append(left, l.task.String)
			continue
		default:
			return 0, errDelegate
		}
		if !created.Valid || created.String == "" {
			left = append(left, l.task.String)
			continue
		}
		prURL := lwNullAny(l.pr)
		if !l.pr.Valid || l.pr.String == "" {
			prURL = lwNullAny(vpr)
		}
		if _, err := c.exec("UPDATE lesson_links SET merged_at=?, pr_url=? WHERE id=?", created.String, prURL, l.id); err != nil {
			return 0, err
		}
	}
	if len(left) > 0 {
		return 0, dieErr(2, *a.id+" has unmerged links: "+strings.Join(left, ", "))
	}
	ts := eventTS(c)
	if _, err := c.exec("UPDATE lessons SET status='applied', applied_at=?, updated=? WHERE id=?", ts, ts, *a.id); err != nil {
		return 0, err
	}
	var detail *string
	if n := pyStrip(lwStr(a.flag("--note"))); n != "" {
		detail = strp(n)
	}
	if err := event(c, "lesson", *a.id, "applied", detail); err != nil {
		return 0, err
	}
	c.out.WriteString(*a.id + " applied\n")
	return 0, nil
}

func lessonVerify(c *ctx, a lessonWriteCmd) (int, error) {
	r, err := lwRow(c, a.id, "factory lesson verify L-xxx --evidence TEXT")
	if err != nil {
		return 0, err
	}
	evp, err := lwRead(c, a.flag("--evidence"))
	if err != nil {
		return 0, err
	}
	ev := pyStrip(lwStr(evp))
	if ev == "" {
		return 0, dieErr(2, "--evidence is required and may not be empty")
	}
	if r.status != "applied" {
		return 0, dieErr(2, *a.id+" is "+r.status+", only an applied lesson can be verified")
	}
	ts := eventTS(c)
	if _, err := c.exec("UPDATE lessons SET status='verified', verified_at=?, verified_by=?, evidence=?, updated=? WHERE id=?",
		ts, c.who, ev, ts, *a.id); err != nil {
		return 0, err
	}
	d := ev
	if rs := []rune(d); len(rs) > 200 {
		d = string(rs[:200])
	}
	if err := event(c, "lesson", *a.id, "verified", strp(d)); err != nil {
		return 0, err
	}
	c.out.WriteString(*a.id + " verified\n")
	return 0, nil
}

func lessonReopen(c *ctx, a lessonWriteCmd) (int, error) {
	r, err := lwRow(c, a.id, "factory lesson reopen L-xxx --reason TEXT [--source ...]")
	if err != nil {
		return 0, err
	}
	rp, err := lwRead(c, a.flag("--reason"))
	if err != nil {
		return 0, err
	}
	reason := pyStrip(lwStr(rp))
	if reason == "" {
		return 0, dieErr(2, "--reason is required and may not be empty")
	}
	newSrc, err := lwSources(a.flag("--source"))
	if err != nil {
		return 0, err
	}
	if r.status != "applied" && r.status != "verified" {
		return 0, dieErr(2, *a.id+" is "+r.status+", only an applied or verified lesson can be reopened")
	}
	srcs := strings.Join(lwDedupe(append(lwSplit(r.sources), newSrc...)), ",")
	if _, err := c.exec("UPDATE lessons SET status='open', recurrences=recurrences+1, verified_at=NULL, verified_by=NULL, "+
		"evidence=NULL, sources=?, updated=? WHERE id=?", srcs, eventTS(c), *a.id); err != nil {
		return 0, err
	}
	if err := event(c, "lesson", *a.id, "recurrence", strp(reason)); err != nil {
		return 0, err
	}
	c.out.WriteString(*a.id + " reopened (recurrences " + strconv.FormatInt(r.recurrences+1, 10) + ")\n")
	return 0, nil
}
