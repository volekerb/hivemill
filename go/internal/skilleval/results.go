package skilleval

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/scout"
)

var markerLine = regexp.MustCompile(`control_marker=([01])`)

// rcRE is re.compile(r"\brc=(-?\d+)"): \b is ASCII here because Python's \b between a word char and "r" is decided
// by Unicode \w; the log is untrusted, so a Unicode letter or digit right before "rc=" is not a word boundary.
var rcRE = regexp.MustCompile(`rc=(-?[0-9\p{Nd}]+)`)

// rcMatch is _RC.search(line) with Python's Unicode \b and \d.
func rcMatch(line string) (string, bool) {
	for _, loc := range rcRE.FindAllStringSubmatchIndex(line, -1) {
		if loc[0] > 0 {
			prev, _ := utf8.DecodeLastRuneInString(line[:loc[0]])
			if isWordRune(prev) {
				continue
			}
		}
		return line[loc[2]:loc[3]], true
	}
	return "", false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Nd, r)
}

// TrialLine is trial_line(r).
func TrialLine(r Trial) string {
	var tok sql.NullInt64
	if r.TokensIn.Valid || r.TokensOut.Valid {
		tok = sql.NullInt64{Int64: r.TokensIn.Int64 + r.TokensOut.Int64, Valid: true}
	}
	s := fmt.Sprintf("%s %s %s: trial %d status=%s passed=%s tokens=%s wall_s=%s skill_invoked=%s", r.CandidateID, r.Task,
		r.Condition, r.ID, r.Status, cell(r.Passed), cell(tok), cell(r.WallS), cell(r.SkillInvoked))
	if m := markerLine.FindStringSubmatch(r.CheckOutput.String); m != nil {
		s += " control_marker=" + m[1]
	}
	if r.Status == "voided" {
		why := "None"
		if r.VoidWhy.Valid {
			why = pyRepr(r.VoidWhy.String)
		}
		reason := "None"
		if r.VoidReason.Valid {
			reason = r.VoidReason.String
		}
		s += fmt.Sprintf(" void_reason=%s void_why=%s", reason, why)
	}
	return s
}

// median is round(statistics.median(vals)) over the non-nil values; ok=false when there are none.
func median(vals []sql.NullInt64) (int64, bool) {
	var v []int64
	for _, x := range vals {
		if x.Valid {
			v = append(v, x.Int64)
		}
	}
	if len(v) == 0 {
		return 0, false
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	n := len(v)
	if n%2 == 1 {
		return v[n/2], true
	}
	return roundEven(float64(v[n/2-1]+v[n/2]) / 2), true
}

func medianCell(vals []sql.NullInt64) string {
	if m, ok := median(vals); ok {
		return fmt.Sprint(m)
	}
	return "-"
}

func totalTokens(r Trial) sql.NullInt64 {
	if !r.TokensIn.Valid && !r.TokensOut.Valid {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: r.TokensIn.Int64 + r.TokensOut.Int64, Valid: true}
}

// SummaryLine is summary_line(cid, rows).
func SummaryLine(cid string, rows []Trial) string {
	var parts []string
	for _, cond := range []string{"with", "without"} {
		var rs []Trial
		for _, r := range rows {
			if r.Condition == cond && in(Terminal, r.Status) && r.Status != "voided" {
				rs = append(rs, r)
			}
		}
		passed := 0
		var toks, walls []sql.NullInt64
		for _, r := range rs {
			if r.Status == "passed" {
				passed++
			}
			toks = append(toks, totalTokens(r))
			walls = append(walls, r.WallS)
		}
		parts = append(parts, fmt.Sprintf("%s: passed %d/%d tasks, median tokens %s, median wall_s %s", cond, passed, len(rs),
			medianCell(toks), medianCell(walls)))
	}
	for _, st := range []string{"pending", "running", "voided"} {
		n := 0
		for _, r := range rows {
			if r.Status == st {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", st, n))
		}
	}
	return cid + " summary: " + strings.Join(parts, "; ")
}

// NeedsStartLog: error / lost trials, and running ones that have burned no tokens yet (the stuck-start signature).
func NeedsStartLog(r Trial) bool {
	noTokens := !r.TokensIn.Valid && !r.TokensOut.Valid
	return r.Status == "error" || r.Status == "lost" || (r.Status == "running" && noTokens)
}

// cleanLine is _clean: ESC, C0 except \t, DEL, C1 and U+FFFD, then every Unicode format character (category Cf).
func cleanLine(line string) string {
	var b strings.Builder
	for _, r := range line {
		if (r <= 0x08) || (r >= 0x0a && r <= 0x1f) || (r >= 0x7f && r <= 0x9f) || r == 0xfffd || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func fdPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

// readStartLog reads the tail of <trial>/log/start.log without following a symlink at the trial dir, log/ or the
// file; nil when it is not a plain readable file.
func readStartLog(trialDir string) []byte {
	const flags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	tfd, err := syscall.Open(trialDir, flags, 0)
	if err != nil {
		return nil
	}
	defer closeFD(tfd)
	dfd, err := syscall.Open(fdPath(tfd)+"/log", flags, 0)
	if err != nil {
		return nil
	}
	defer closeFD(dfd)
	fd, err := syscall.Open(fdPath(dfd)+"/start.log", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer closeFD(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil
	}
	off := st.Size - StartLogRead
	if off < 0 {
		off = 0
	}
	buf := make([]byte, StartLogRead)
	n, err := syscall.Pread(fd, buf, off)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// closeFD closes a read-only descriptor; a failed close of one opened for reading loses nothing.
func closeFD(fd int) {
	if err := syscall.Close(fd); err != nil {
		return
	}
}

var tokenShape = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]+`)

// StartLogLines is start_log_lines(r, tail): the indented last rc and at most `tail` lines of <trial>/log/start.log,
// token-shaped strings redacted. Empty when the trial needs none or has no regular-file log.
func StartLogLines(r Trial, n int) []string {
	if !NeedsStartLog(r) || !r.Sandbox.Valid || r.Sandbox.String == "" {
		return nil
	}
	raw := readStartLog(r.Sandbox.String)
	if raw == nil {
		return nil
	}
	// split on \n only (str.splitlines also breaks on \x0b \x0c \x1c-\x1e \x85), strip controls and format chars, then redact
	var lines []string
	for _, l := range strings.Split(strings.ToValidUTF8(string(raw), "�"), "\n") {
		if c := cleanLine(l); pyStrip(c) != "" {
			lines = append(lines, c)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	from := len(lines) - n
	if from < 0 {
		from = 0
	}
	var shown []string
	for _, l := range lines[from:] {
		l = tokenShape.ReplaceAllString(l, "[redacted-token]")
		if rs := []rune(l); len(rs) > 300 {
			l = string(rs[:300])
		}
		shown = append(shown, l)
	}
	out := []string{"  start.log:"}
	for _, l := range shown {
		out = append(out, "    "+l)
	}
	has := false
	for _, l := range shown {
		if _, ok := rcMatch(l); ok {
			has = true
			break
		}
	}
	if !has {
		last, found := "", false
		for _, l := range lines {
			if rc, ok := rcMatch(l); ok {
				last, found = rc, true
			}
		}
		if found {
			out = append(out[:1], append([]string{"    last rc=" + last}, out[1:]...)...)
		}
	}
	return out
}

// IsControl is true for the reserved positive-control candidate.
func IsControl(db *sql.DB, cid string) (bool, error) {
	var key string
	err := db.QueryRow("SELECT key FROM scout_candidates WHERE id=?", cid).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up candidate %s: %w", cid, err)
	}
	return key == scout.ControlKey, nil
}

// ListLines is list_lines: the same text as ResultLines.
func ListLines(db *sql.DB, candidate string) ([]string, error) { return ResultLines(db, candidate) }

// ResultLines is result_lines(con, candidate): per candidate every trial (with its start log), then the summary.
func ResultLines(db *sql.DB, candidate string) ([]string, error) {
	tail := "ORDER BY candidate_id, task, condition"
	var args []any
	if candidate != "" {
		tail, args = "WHERE candidate_id=? "+tail, []any{candidate}
	}
	rows, err := QueryTrials(db, tail, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, r := range rows {
		if !seen[r.CandidateID] {
			seen[r.CandidateID] = true
			ids = append(ids, r.CandidateID)
		}
	}
	sort.Strings(ids)
	var out []string
	for _, cid := range ids {
		var mine []Trial
		for _, r := range rows {
			if r.CandidateID == cid {
				mine = append(mine, r)
			}
		}
		for _, r := range mine {
			out = append(out, TrialLine(r))
			out = append(out, StartLogLines(r, StartTail)...)
		}
		out = append(out, SummaryLine(cid, mine))
		ctl, err := IsControl(db, cid)
		if err != nil {
			return nil, err
		}
		if ctl {
			out = append(out, fmt.Sprintf("%s control: positive control (%s), not a find; healthy = a with-trial has "+
				"skill_invoked=1 or control_marker=1", cid, scout.ControlKey))
		}
	}
	return out, nil
}
