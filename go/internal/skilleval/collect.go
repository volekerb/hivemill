package skilleval

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/tokens"
)

// TempDone is temp_done(fh, agent): does the trial's temp DB show the evaluator agent done? Any read problem means
// "not done". ended is the agent's ended stamp.
func TempDone(fh, agent string) (done bool, ended string) {
	path := FhDB(fh)
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return false, ""
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)") // sqlite3.connect(timeout=5)
	if err != nil {
		return false, ""
	}
	defer db.Close() // a read-only handle: nothing to lose on close
	var status string
	var end sql.NullString
	if err := db.QueryRow("SELECT status, ended FROM agents WHERE name=?", agent).Scan(&status, &end); err != nil || status != "done" {
		return false, ""
	}
	return true, end.String
}

// CleanProjects drops everything under projects that is not a plain file or directory (symlink, fifo, socket): the
// transcripts were written by the evaluator, so nothing but regular files reach a parser.
func CleanProjects(projects string) error {
	var bad []string
	err := filepath.WalkDir(projects, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if p != projects && !d.Type().IsRegular() && !d.IsDir() {
			bad = append(bad, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", projects, err)
	}
	for _, p := range bad {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// splitLines is str.splitlines() for the separators Python knows.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			start = i + n
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				n++
			}
			start = i + n
		}
		i += n
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// SkillNames is skill_names(root, trial): the dir name of every SKILL.md of the candidate's vendored plugin and its
// `name:` line, plus the plugin dir.
func SkillNames(root string, t Trial) (map[string]bool, string, error) {
	names := map[string]bool{}
	meta, ok := scout.Vendored(root, t.CandidateID, t.VendorSha)
	if !ok {
		return names, "", nil
	}
	plugin := sandbox.Realpath(filepath.Join(scout.VendorDir(root, t.CandidateID, t.VendorSha), meta.Plugin))
	var files []string
	err := filepath.WalkDir(plugin, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && d.Name() == "SKILL.md" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("list SKILL.md under %s: %w", plugin, err)
	}
	for _, f := range files {
		names[filepath.Base(filepath.Dir(f))] = true
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			continue // an unreadable file adds no name (Python: except OSError: pass)
		}
		lines := splitLines(strings.ToValidUTF8(string(b), "�"))
		if len(lines) > 12 {
			lines = lines[:12]
		}
		for _, line := range lines {
			if v, found := strings.CutPrefix(line, "name:"); found {
				names[strings.Trim(pyStrip(v), "\"'")] = true
			}
		}
	}
	return names, plugin, nil
}

// RegisterControl inserts or refreshes the reserved control candidate row and returns it. head_sha is a content hash
// of the control dir; status triaged and the reserved key keep it out of the scout's finds.
func RegisterControl(db *sql.DB, stamp, controlDir string) (scout.Candidate, error) {
	sha, err := scout.ContentSha(controlDir)
	if err != nil {
		return scout.Candidate{}, fmt.Errorf("hash control dir %s: %w", controlDir, err)
	}
	var id string
	err = db.QueryRow("SELECT id FROM scout_candidates WHERE key=?", scout.ControlKey).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		cid, nerr := nextCandidateID(db)
		if nerr != nil {
			return scout.Candidate{}, nerr
		}
		_, err = db.Exec("INSERT INTO scout_candidates(id,key,kind,head_sha,licence,install_risk,sources,first_seen,last_seen,status) "+
			"VALUES(?,?,'skill',?,'repo-owned','prompt-only','[\"control\"]',?,?,'triaged')", cid, scout.ControlKey, sha, stamp, stamp)
		id = cid
	case err == nil:
		_, err = db.Exec("UPDATE scout_candidates SET head_sha=?, install_risk='prompt-only', last_seen=?, "+
			"status=CASE WHEN status IN ('new','needs-owner','dismissed') THEN 'triaged' ELSE status END WHERE id=?", sha, stamp, id)
	}
	if err != nil {
		return scout.Candidate{}, fmt.Errorf("register control candidate: %w", err)
	}
	c, ok, err := scout.GetCandidate(db, id)
	if err != nil || !ok {
		return scout.Candidate{}, fmt.Errorf("reload control candidate %s: %v", id, err)
	}
	return c, nil
}

// nextCandidateID is _next_candidate_id: C-nnnn after the highest existing number.
func nextCandidateID(db *sql.DB) (string, error) {
	rows, err := db.Query("SELECT id FROM scout_candidates")
	if err != nil {
		return "", fmt.Errorf("list candidate ids: %w", err)
	}
	defer rows.Close()
	n := int64(0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("scan candidate id: %w", err)
		}
		if _, num, ok := strings.Cut(id, "-"); ok {
			if v, perr := strconv.ParseInt(num, 10, 64); perr == nil && v > n {
				n = v
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read candidate ids: %w", err)
	}
	return fmt.Sprintf("C-%04d", n+1), nil
}

// ControlMarker is 1 if work/SKILL_MARKER.txt is a regular file holding exactly the marker text (one trailing
// newline allowed), else 0. work/ is untrusted: no symlink is followed and the read is capped.
func ControlMarker(work string) int {
	fd, err := syscall.Open(filepath.Join(work, MarkerFile), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0
	}
	defer closeFD(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return 0
	}
	buf := make([]byte, MarkerRead)
	n, err := syscall.Read(fd, buf)
	if err != nil {
		return 0
	}
	if got := string(buf[:n]); got == MarkerText || got == MarkerText+"\n" {
		return 1
	}
	return 0
}

// pyStrOf is str(v) for a decoded JSON value in the places the transcript parser needs it.
func pyStrOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v) // lists and dicts never equal a skill name or end in SKILL.md
}

func orEmpty(v any) any {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if x == "" {
			return ""
		}
	case bool:
		if !x {
			return ""
		}
	case json.Number:
		if f, err := x.Float64(); err == nil && f == 0 {
			return ""
		}
	case []any:
		if len(x) == 0 {
			return ""
		}
	case map[string]any:
		if len(x) == 0 {
			return ""
		}
	}
	return v
}

// parentName is Path(p).parent.name for a POSIX path.
func parentName(p string) string {
	dir := path.Dir(path.Clean(p))
	if dir == "." || dir == "/" {
		return ""
	}
	return path.Base(dir)
}

// SkillWasInvoked is skill_was_invoked: 1 if a transcript has a Skill tool_use naming one of names or a Read of one
// of the plugin's SKILL.md files, else 0.
func SkillWasInvoked(projects string, names map[string]bool, plugin string) (int, error) {
	var files []string
	err := filepath.WalkDir(projects, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("list transcripts under %s: %w", projects, err)
	}
	sort.Strings(files)
	for _, f := range files {
		hit, err := invokedIn(f, names, plugin)
		if err != nil {
			continue // an unreadable transcript is skipped (Python: except OSError: continue)
		}
		if hit {
			return 1, nil
		}
	}
	return 0, nil
}

func invokedIn(file string, names map[string]bool, plugin string) (bool, error) {
	fh, err := os.Open(file)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", file, err)
	}
	defer fh.Close() // read-only
	r := bufio.NewReaderSize(fh, 64*1024)
	for {
		line, err := readLine(r)
		if len(line) > 0 && bytes.Contains(line, []byte(`"tool_use"`)) && invokedLine(line, names, plugin) {
			return true, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("read %s: %w", file, err)
		}
	}
}

// readLine reads up to and including \n; a line longer than MaxTranscriptLine is dropped (returned empty).
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	skipping := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !skipping {
			buf = append(buf, chunk...)
			if len(buf) > MaxTranscriptLine {
				buf, skipping = nil, true
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if skipping {
			return nil, err
		}
		return buf, err
	}
}

func invokedLine(line []byte, names map[string]bool, plugin string) bool {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var d any
	if err := dec.Decode(&d); err != nil {
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false // json.loads refuses trailing data
	}
	obj, _ := d.(map[string]any)
	msg, _ := obj["message"].(map[string]any)
	content, _ := msg["content"].([]any)
	for _, bAny := range content {
		b, ok := bAny.(map[string]any)
		if !ok || b["type"] != "tool_use" {
			continue
		}
		inp, _ := b["input"].(map[string]any)
		switch b["name"] {
		case "Skill":
			v := orEmpty(inp["skill"])
			if v == "" {
				v = orEmpty(inp["name"])
			}
			sk := ""
			if v != "" {
				sk = pyStrOf(v)
			}
			last := sk
			if i := strings.LastIndex(sk, ":"); i >= 0 {
				last = sk[i+1:]
			}
			if names[sk] || names[last] {
				return true
			}
		case "Read":
			p := ""
			if v := orEmpty(inp["file_path"]); v != "" {
				p = pyStrOf(v)
			}
			if strings.HasSuffix(p, "SKILL.md") &&
				((plugin != "" && strings.HasPrefix(p, plugin+"/")) || names[parentName(p)]) {
				return true
			}
		}
	}
	return false
}

// put copies one file into work/ without following anything the evaluator planted: a symlink at the destination or
// on its parent chain is replaced, so the write cannot land outside work/.
func put(work, rel, src string) error {
	dest := filepath.Join(work, rel)
	cur := work
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		if st, err := os.Lstat(cur); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.IsDir()) {
			if err := os.Remove(cur); err != nil {
				return fmt.Errorf("replace %s: %w", cur, err)
			}
		}
		if err := os.Mkdir(cur, 0o777); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", cur, err)
		}
	}
	if st, err := os.Lstat(dest); err == nil {
		if st.IsDir() {
			if err := os.RemoveAll(dest); err != nil {
				return fmt.Errorf("remove %s: %w", dest, err)
			}
		} else if err := os.Remove(dest); err != nil {
			return fmt.Errorf("remove %s: %w", dest, err)
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dest, data, 0o666); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// StageChecker copies hidden/ and check.py into work/ (data copy only; nothing runs here).
func StageChecker(taskDir, work string) error {
	if st, err := os.Lstat(work); err != nil || !st.IsDir() {
		return cerr("%s is not a plain directory", work)
	}
	hidden := filepath.Join(taskDir, "hidden")
	if st, err := os.Stat(hidden); err == nil && st.IsDir() {
		var files []string
		err := filepath.WalkDir(hidden, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				if st, serr := os.Stat(p); serr == nil && st.Mode().IsRegular() {
					files = append(files, p)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("list %s: %w", hidden, err)
		}
		sort.Strings(files)
		for _, f := range files {
			rel, err := filepath.Rel(hidden, f)
			if err != nil {
				return fmt.Errorf("relative path of %s: %w", f, err)
			}
			if err := put(work, rel, f); err != nil {
				return err
			}
		}
	}
	return put(work, "check.py", filepath.Join(taskDir, "check.py"))
}

// containedCfg is the allow-list cfg of run_contained for a cwd inside the trial dir: tmpfs over the real home and
// the runtime dir, bound back only the trial dir (rw). Siblings stay hidden even when the trials root lies outside
// every hide tree.
func containedCfg(trialDir, home, unshare string) (*sandbox.Cfg, error) {
	if home == "" {
		home = sandbox.RealHome()
	}
	home = sandbox.Realpath(home)
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	var hide []string
	for _, d := range []string{home, runtime} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			hide = append(hide, sandbox.Realpath(d))
		}
	}
	trial := sandbox.Realpath(trialDir)
	root := filepath.Dir(trial)
	var masks []string
	inHide := false
	for _, h := range hide {
		inHide = inHide || root == h || strings.HasPrefix(root, strings.TrimRight(h, "/")+"/")
	}
	if !inHide {
		masks = []string{root}
	}
	o := sandbox.NewOpts(trial)
	o.Home, o.TrialsRoot, o.Mode = home, root, "allow"
	o.HideDirs, o.HideFiles, o.Rw, o.Masks = hide, nil, []string{trial}, masks
	o.Unshare, o.Cwd = unshare, filepath.Join(trial, "work")
	return sandbox.MakeCfg(o)
}

// RunChecker returns (status, passed, output). It is the only place check.py runs, and only through
// sandbox.RunContained.
func RunChecker(d, task, tasksDir, home, unshare string) (status string, passed sql.NullInt64, output string) {
	if unshare == "" {
		unshare = "unshare"
	}
	if ok, why := sandbox.UsernsAvailable(unshare); !ok {
		return "error", sql.NullInt64{}, "checker not run: user namespaces unavailable, no uncontained fallback: " + why
	}
	work := filepath.Join(d, "work")
	if err := StageChecker(filepath.Join(tasksDir, task), work); err != nil {
		return "error", sql.NullInt64{}, "checker not run: " + err.Error()
	}
	cfg, err := containedCfg(d, home, unshare)
	if err != nil {
		return "error", sql.NullInt64{}, "checker not run: " + err.Error()
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return "error", sql.NullInt64{}, "checker not run: " + err.Error()
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	env := []string{"PATH=" + path, "HOME=" + filepath.Join(d, "home")}
	res, err := sandbox.RunContained(cfg, []string{py, "check.py"}, work, env, CheckTimeout)
	if err != nil {
		return "error", sql.NullInt64{}, "checker not run: " + err.Error()
	}
	if res.TimedOut {
		return "failed", sql.NullInt64{Int64: 0, Valid: true}, fmt.Sprintf("check.py exceeded %d s and was killed (timeout)\n%s", int(CheckTimeout.Seconds()), res.Output)
	}
	if res.ReturnCode == 0 {
		return "passed", sql.NullInt64{Int64: 1, Valid: true}, res.Output
	}
	return "failed", sql.NullInt64{Int64: 0, Valid: true}, res.Output
}

// Collector holds the repo-relative paths the collect code needs.
type Collector struct {
	TasksDir string // <repo>/skilleval/tasks
}

// CollectTrial collects one running trial under a per-trial lock: a concurrent collect of the same trial is refused
// so nobody removes the sandbox under a running checker.
func (c Collector) CollectTrial(db *sql.DB, id int64, root string, api API, reason string) (Trial, error) {
	lockdir := filepath.Join(sandbox.Realpath(root), "locks")
	if err := os.MkdirAll(lockdir, 0o777); err != nil {
		return Trial{}, fmt.Errorf("create %s: %w", lockdir, err)
	}
	lk, err := os.OpenFile(filepath.Join(lockdir, sandbox.TrialName(int(id))+".lock"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return Trial{}, fmt.Errorf("open trial lock: %w", err)
	}
	defer lk.Close() // closing releases the flock; the file holds no data
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return Trial{}, cerr("trial %d is being collected by another process; retry shortly", id)
	}
	return c.collect(db, id, root, api, reason)
}

func (c Collector) collect(db *sql.DB, id int64, root string, api API, reason string) (Trial, error) {
	if reason != "" && !in(Reasons, reason) {
		return Trial{}, cerr("reason must be one of %s", strings.Join(Reasons, ", "))
	}
	row, ok, err := GetTrial(db, id)
	if err != nil {
		return Trial{}, err
	}
	if !ok {
		return Trial{}, cerr("no trial %d", id)
	}
	if in(Terminal, row.Status) {
		return row, nil
	}
	if row.Status != "running" {
		return Trial{}, cerr("trial %d is %s, not running: nothing to collect", row.ID, row.Status)
	}
	d := TrialDir(root, row)
	agent := row.Agent.String
	type agentRow struct {
		Status, Started string
		StartedValid    bool
	}
	var live *agentRow
	if agent != "" {
		var st string
		var started sql.NullString
		err := db.QueryRow("SELECT status, started FROM agents WHERE name=?", agent).Scan(&st, &started)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Trial{}, fmt.Errorf("load agent %s: %w", agent, err)
		}
		if err == nil {
			live = &agentRow{Status: st, Started: started.String, StartedValid: started.Valid}
		}
	}
	done, doneAt := false, ""
	if d != "" && agent != "" {
		done, doneAt = TempDone(filepath.Join(d, "fh"), agent)
	}
	lost := live == nil || live.Status != "live"
	if reason == "" && !done && !lost {
		return Trial{}, cerr("trial %d is still running (agent %s live, not done in its temp DB); "+
			"pass --reason timeout|over-budget|cancelled to end it", row.ID, agent)
	}
	if live != nil && live.Status == "live" {
		// EVERY path stops the evaluator first: a live one could rewrite work/check.py or race the staging and the
		// transcript parse. Only a confirmed stop (or an agent that finished by itself meanwhile) lets us go on.
		if _, err := db.Exec("UPDATE agents SET stop_requested_at=? WHERE name=? AND status='live' AND stop_requested_at IS NULL", api.Now(), agent); err != nil {
			return Trial{}, fmt.Errorf("request stop of %s: %w", agent, err)
		}
		var seen sql.NullString
		if err := db.QueryRow("SELECT stop_attempt_at FROM agents WHERE name=?", agent).Scan(&seen); err != nil {
			return Trial{}, fmt.Errorf("read stop_attempt_at of %s: %w", agent, err)
		}
		res, note, err := api.ForceStop(db, agent, "now", seen)
		if err != nil {
			return Trial{}, fmt.Errorf("force_stop %s: %w", agent, err)
		}
		var cur sql.NullString
		curErr := db.QueryRow("SELECT status FROM agents WHERE name=?", agent).Scan(&cur)
		if curErr != nil && !errors.Is(curErr, sql.ErrNoRows) {
			return Trial{}, fmt.Errorf("re-read agent %s: %w", agent, curErr)
		}
		if !(res == "stopped" || (res == "skip" && curErr == nil && cur.String != "live")) {
			why := note
			if why == "" {
				why = "the evaluator may still be running"
			}
			msg := fmt.Sprintf("force_stop of %s %s: %s; checker not run, trial left running: retry collect once the agent is stopped",
				agent, res, why)
			if _, err := db.Exec("UPDATE skill_trials SET check_output=? WHERE id=? AND status='running'", tail(msg, OutputMax), row.ID); err != nil {
				return Trial{}, fmt.Errorf("record stop failure of trial %d: %w", row.ID, err)
			}
			return Trial{}, cerr("%s", msg)
		}
	}
	if d != "" {
		StopProxy(d)
	}
	end := api.Now()
	if _, err := db.Exec("UPDATE agents SET status='done', ended=?, stop_requested_at=NULL, stop_attempt_at=NULL, "+
		"stop_failed_kind=NULL WHERE name=? AND status='live'", end, agent); err != nil {
		return Trial{}, fmt.Errorf("mark agent %s done: %w", agent, err)
	}
	status, output := "", ""
	var passed sql.NullInt64
	switch {
	case d == "":
		status, output = "error", fmt.Sprintf("sandbox of trial %d is gone; nothing to check", row.ID)
	case reason == "" && !done:
		status, output = "error", fmt.Sprintf("agent %s exited without finishing (no done in its temp DB); checker not run", agent)
	case reason == "cancelled":
		status, passed = "cancelled", sql.NullInt64{Int64: 0, Valid: true}
	default:
		status, passed, output = RunChecker(d, row.Task, c.TasksDir, api.Home(), api.Unshare())
		if reason != "" { // timeout / over-budget: the status says why it ended; passed stays 0
			output = fmt.Sprintf("[%s] checker status %s\n%s", reason, status, output)
			status, passed = reason, sql.NullInt64{Int64: 0, Valid: true}
		}
	}
	var tokIn, tokOut, cacheRead, cacheWrite sql.NullInt64
	var invoked sql.NullInt64
	projects := ""
	if d != "" {
		projects = filepath.Join(d, "cfg", "projects")
	}
	if st, serr := os.Lstat(projects); projects != "" && serr == nil && st.IsDir() {
		if err := CleanProjects(projects); err != nil {
			return Trial{}, err
		}
		u := tokens.UsageTotals(projects)
		tokIn, tokOut = sql.NullInt64{Int64: u.Input, Valid: true}, sql.NullInt64{Int64: u.Output, Valid: true}
		cacheRead, cacheWrite = sql.NullInt64{Int64: u.CacheRead, Valid: true}, sql.NullInt64{Int64: u.CacheWrite, Valid: true}
		if row.Condition == "with" {
			names, plugin, err := SkillNames(root, row)
			if err != nil {
				return Trial{}, err
			}
			n, err := SkillWasInvoked(projects, names, plugin)
			if err != nil {
				return Trial{}, err
			}
			invoked = sql.NullInt64{Int64: int64(n), Valid: true}
		}
	} else if d != "" {
		z := sql.NullInt64{Int64: 0, Valid: true}
		tokIn, tokOut, cacheRead, cacheWrite = z, z, z, z
	}
	if d != "" {
		ctl, err := IsControl(db, row.CandidateID)
		if err != nil {
			return Trial{}, err
		}
		if ctl { // positive control: did the skill's effect show up in work/?
			prefix := ""
			if output != "" {
				prefix = tail(output, OutputMax) + "\n"
			}
			output = fmt.Sprintf("%scontrol_marker=%d", prefix, ControlMarker(filepath.Join(d, "work")))
		}
	}
	started := row.Started.String
	if live != nil {
		started = live.Started
	}
	endForWall := end
	if done && doneAt != "" {
		endForWall = doneAt
	}
	var wall sql.NullInt64
	if w, ok := wallSeconds(started, endForWall); ok {
		wall = sql.NullInt64{Int64: w, Valid: true}
	}
	res, err := db.Exec("UPDATE skill_trials SET status=?, passed=?, ended=?, wall_s=?, tokens_in=?, tokens_out=?, cache_read=?, "+
		"cache_write=?, skill_invoked=?, check_output=? WHERE id=? AND status='running'",
		status, passed, end, wall, tokIn, tokOut, cacheRead, cacheWrite, invoked, tail(output, OutputMax), row.ID)
	if err != nil {
		return Trial{}, fmt.Errorf("record trial %d: %w", row.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Trial{}, fmt.Errorf("count updated trials: %w", err)
	}
	if n == 1 && d != "" && in(DeleteSandbx, status) {
		// Python: shutil.rmtree(d, ignore_errors=True). The row is already final; a leftover dir is cleaned later.
		if err := os.RemoveAll(d); err != nil {
			_ = err // best effort by design (see above)
		}
	}
	if n == 1 {
		if err := api.Event(db, "skilleval", row.CandidateID, "collect", fmt.Sprintf("trial %d %s", row.ID, status)); err != nil {
			return Trial{}, fmt.Errorf("record collect event: %w", err)
		}
	}
	out, _, err := GetTrial(db, row.ID)
	return out, err
}
