package tick

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"factory/go/internal/store"
)

// Unit 26 claude_audit (_tick_u_claude_audit, claude_outside_spawn, sandbox_escape_close, prune_escape_throttle in the
// Python CLI): a /proc scan for claude processes under a live agent that `factory spawn` did not start, and for any
// agent claude resumed without `--system-prompt-snapshot off`. One event and one director alert per pid. The scan is
// read-only; the one exception is Python's own sandbox-escape pane close (sandboxEscapeClose, through the host
// client), which the audit docstring also glosses as "never kills".

const (
	claudeAuditUnit     = "claude_audit"
	claudeAuditEveryS   = 300
	escapeRowTTLS       = 7 * 86400 // ESCAPE_ROW_TTL_S
	maxParentWalk       = 64
	claudeOutsidePrefix = "claude-outside:"
)

func init() { registerGo(claudeAuditUnit, runClaudeAudit) }

// resumeSnapshotOff is RESUME_PROMPT_FLAGS.
var resumeSnapshotOff = []string{"--system-prompt-snapshot", "off"}

// snapshotOff is _snapshot_off: the two flags side by side, or the --system-prompt-snapshot=off form.
func snapshotOff(argv []string) bool {
	n := len(resumeSnapshotOff)
	for i := 0; i+n <= len(argv); i++ {
		if argv[i] == resumeSnapshotOff[0] && argv[i+1] == resumeSnapshotOff[1] {
			return true
		}
	}
	return containsStr(argv, strings.Join(resumeSnapshotOff, "="))
}

// userNSLink is _userns_link: readlink of <proc>/<pid>/ns/user, "" when unreadable.
func userNSLink(root, pid string) string {
	l, err := os.Readlink(filepath.Join(root, pid, "ns", "user"))
	if err != nil {
		return ""
	}
	return l
}

func throttleExists(db *sql.DB, key string) (bool, error) {
	var one int
	err := db.QueryRow("SELECT 1 FROM throttle WHERE key=?", key).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read throttle %s: %w", key, err)
	}
	return true, nil
}

// pruneEscapeThrottle is prune_escape_throttle: delete `sandbox-escaped:<pid>:<start>` rows whose process is gone (no
// entry, or another start time) or whose ts is older than 7 days; `:None` rows go by age only.
func pruneEscapeThrottle(db *sql.DB, root string, nowTS float64) error {
	rs, err := db.Query("SELECT key, ts FROM throttle WHERE key LIKE 'sandbox-escaped:%'")
	if err != nil {
		return fmt.Errorf("read escape throttle rows: %w", err)
	}
	var gone []string
	for rs.Next() {
		var key string
		var ts sql.NullString
		if err := rs.Scan(&key, &ts); err != nil {
			rs.Close()
			return fmt.Errorf("read escape throttle rows: %w", err)
		}
		if !ts.Valid {
			rs.Close()
			return fmt.Errorf("throttle %s has a NULL ts (Python float(None) raises TypeError)", key)
		}
		f, ok := parsePyFloat(ts.String)
		old := !ok || nowTS-f > escapeRowTTLS
		j := strings.LastIndex(key, ":")
		i := strings.LastIndex(key[:j], ":")
		if i < 0 {
			continue // not <prefix>:<pid>:<start>
		}
		pid, start := key[i+1:j], key[j+1:]
		dead := false
		if start != "None" {
			got, ok := procStart(root, pid)
			dead = !ok || got != start
		}
		if old || dead {
			gone = append(gone, key)
		}
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return fmt.Errorf("read escape throttle rows: %w", err)
	}
	rs.Close() // one connection: the DELETEs wait for the SELECT to finish
	for _, k := range gone {
		if _, err := db.Exec("DELETE FROM throttle WHERE key=?", k); err != nil {
			return fmt.Errorf("delete throttle %s: %w", k, err)
		}
	}
	return nil
}

// sandboxEscapeClose is sandbox_escape_close (fail closed): a sandboxed agent's claude whose user namespace equals the
// host's ran outside the sandbox. Its pane is closed through the host client (the call force_stop uses), one event and
// one director alert per process (pid + start time, read before the close). An unreadable link decides nothing.
func sandboxEscapeClose(r *reapDB, root, name, pane string, pid int) error {
	pidS := strconv.Itoa(pid)
	mine := userNSLink(root, pidS)
	host := userNSLink(root, "1")
	if host == "" {
		host = userNSLink(root, "self")
	}
	if mine == "" || host == "" || mine != host {
		return nil
	}
	start, ok := procStart(root, pidS) // before the close: it kills claude and /proc/<pid> goes with it
	startS := "None"
	if ok {
		startS = start
	}
	st := "no-pane"
	if pane != "" {
		hc, hs := newHerdrClient(r)
		s, _, err := hc.ClosePane(name, pane)
		if err != nil {
			return fmt.Errorf("close pane of %s: %w", name, err)
		}
		if err := hs.Err(); err != nil {
			return fmt.Errorf("close pane of %s: %w", name, err)
		}
		st = s
	}
	key := "sandbox-escaped:" + pidS + ":" + startS // start time: a reused pid is a new process, a new alert
	seen, err := throttleExists(r.DB, key)
	if err != nil || seen {
		return err
	}
	if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?)", key, store.PyFloatRepr(reapTime())); err != nil {
		return fmt.Errorf("write throttle %s: %w", key, err)
	}
	if err := r.ownerEvent("agent", name, "sandbox-escaped", store.Ordered{{K: "name", V: name}, {K: "pid", V: pid}}); err != nil {
		return err
	}
	var outcome string
	switch st {
	case "closed":
		outcome = "Its pane was closed."
	case "not-found":
		outcome = "Its pane was NOT closed: the host lists no pane under this name, so the process may still run " +
			"outside the sandbox. Find and stop it."
	case "unknown":
		outcome = "Its pane was NOT closed: the host could not be asked, so the process may still run outside the " +
			"sandbox. Close the pane by hand or retry."
	default:
		outcome = "It has no pane row to close; stop the process."
	}
	body := fmt.Sprintf("%s is a sandboxed agent but its claude (pid %d) runs in the host user namespace, outside the "+
		"sandbox. %s Check its shell rc and multiplexer auto-resume, then relaunch.", name, pid, outcome)
	if _, _, err := r.Send(store.Msg{To: "director", Subject: "sandbox escaped: " + name + " closed", Body: &body,
		Kind: "alert", Sender: reapActor}); err != nil {
		return fmt.Errorf("sandbox escape alert: %w", err)
	}
	return nil
}

func strOrNone(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return s.String
}

func optStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// claudeOutsideSpawn is claude_outside_spawn. Any error it returns becomes a claude-audit-failed event in the unit.
func claudeOutsideSpawn(r *reapDB) error {
	last, has, err := throttleTS(r.DB, "claude-audit")
	if err != nil {
		return err
	}
	now := reapTime()
	if has && now-last < claudeAuditEveryS {
		return nil
	}
	if err := setThrottle(r.DB, "claude-audit", now); err != nil {
		return err
	}
	root := procRoot(r)
	if err := pruneEscapeThrottle(r.DB, root, reapTime()); err != nil {
		return err
	}
	found := map[int]procEnt{}
	for p, v := range scanClaude(root) { // an unreadable proc dir is no process, as in Python
		if isClaudeCmd(v.Argv) {
			found[p] = v
		}
	}
	if len(found) == 0 {
		return nil
	}
	rows, err := liveAgentRows(r.DB)
	if err != nil {
		return err
	}
	argvs := map[int][]string{}
	for p, v := range found {
		argvs[p] = v.Argv
	}
	agents := map[int]agentRow{} // pid -> the agent row whose claude it is
	for _, ar := range rows {
		for _, p := range claudeProcs(root, ar.Session.String, argvs) {
			agents[p.Pid] = ar
		}
	}
	for _, pid := range sortedPids(agents) {
		ar := agents[pid]
		if sa, ok := store.SpawnArgsMap(ar.SpawnArgs); ok && sa["sandbox"] == true {
			if err := sandboxEscapeClose(r, root, ar.Name, ar.Pane.String, pid); err != nil {
				return err
			}
		}
	}
	for _, pid := range sortedPids(found) {
		ent := found[pid]
		argv := ent.Argv
		owner, isOwned := agents[pid]
		var reasons []string
		if !isOwned {
			cur, curOK := ent.Ppid, ent.HasPpid
			for i := 0; i < maxParentWalk; i++ {
				if !curOK || cur <= 1 {
					break
				}
				if a, in := agents[cur]; in {
					owner, isOwned = a, true
					reasons = append(reasons, "outside-spawn")
					break
				}
				if f, in := found[cur]; in {
					cur, curOK = f.Ppid, f.HasPpid
				} else {
					cur, curOK = readPpid(root, strconv.Itoa(cur))
				}
			}
		}
		if !isOwned {
			continue
		}
		if containsStr(argv, "--resume") && !snapshotOff(argv) {
			reasons = append(reasons, "resume-without-snapshot-off")
		}
		if len(reasons) == 0 {
			continue
		}
		if err := reportOutside(r, pid, argv, owner, reasons); err != nil {
			return err
		}
	}
	return nil
}

// reportOutside records one finding: the throttle row that makes it once per pid, the event and the director alert.
func reportOutside(r *reapDB, pid int, argv []string, owner agentRow, reasons []string) error {
	key := claudeOutsidePrefix + strconv.Itoa(pid)
	seen, err := throttleExists(r.DB, key)
	if err != nil || seen {
		return err
	}
	if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?)", key, store.PyFloatRepr(reapTime())); err != nil {
		return fmt.Errorf("write throttle %s: %w", key, err)
	}
	head := argv
	if len(head) > 8 {
		head = head[:8]
	}
	head = append([]string(nil), head...)
	for i, x := range argv { // keep the session id of a resume even when it sits beyond the 8-token head
		if x == "--resume" {
			if i+1 >= len(head) {
				end := i + 2
				if end > len(argv) {
					end = len(argv)
				}
				head = append(head, argv[i:end]...)
			}
			break
		}
	}
	av := make([]any, len(head))
	for i, x := range head {
		av[i] = x
	}
	rs := make([]any, len(reasons))
	for i, x := range reasons {
		rs[i] = x
	}
	if err := r.ownerEvent("agent", owner.Name, "claude-outside-spawn",
		store.Ordered{{K: "pid", V: pid}, {K: "argv", V: av}, {K: "reasons", V: rs}}); err != nil {
		return err
	}
	outside := containsStr(reasons, "outside-spawn")
	var what, subject, tail string
	if outside {
		what, subject = "claude started outside factory spawn", "claude outside factory spawn under "+owner.Name
		tail = "Only factory spawn starts Claude."
	} else {
		what = "claude resumed without --system-prompt-snapshot off (stale recorded system prompt)"
		subject = "claude resumed without --system-prompt-snapshot off under " + owner.Name
		tail = "Resumed without --system-prompt-snapshot off: stale role prompt; fix with factory relaunch " +
			owner.Name + " --when-idle."
	}
	body := fmt.Sprintf("%s under %s (task %s): pid %d %s. %s", what, owner.Name, strOrNone(owner.Task), pid,
		strings.Join(head, " "), tail)
	if _, _, err := r.Send(store.Msg{To: "director", Subject: subject, Body: &body, Kind: "alert",
		Goal: optStr(owner.Goal), Task: optStr(owner.Task), Sender: reapActor}); err != nil {
		return fmt.Errorf("claude audit alert: %w", err)
	}
	return nil
}

// runClaudeAudit is _tick_u_claude_audit: the audit must never stop the tick, so an error becomes one
// claude-audit-failed event. Only a failure to write that event ends the tick (Python's would raise too).
func runClaudeAudit(u *UnitCtx) error {
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return fmt.Errorf("claude_audit: %w", err)
	}
	defer closeDB()
	r, err := newReapDB(db)
	if err != nil {
		return fmt.Errorf("claude_audit: %w", err)
	}
	if aerr := claudeOutsideSpawn(r); aerr != nil {
		if err := r.ownerEvent("tick", "claude-audit", "claude-audit-failed", aerr.Error()); err != nil {
			return fmt.Errorf("claude_audit: %w", err)
		}
	}
	return nil
}
