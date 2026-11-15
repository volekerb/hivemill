package cli

import (
	"database/sql"
	"errors"
	"regexp"
	"runtime"
	"strings"

	"factory/go/internal/gate"
)

// Worker detection: the Go port of caller_is_worker / _detect_worker / _ancestry_worker / compact_view in
// bin/factory_cli.py. Go never writes, so the one Python path that records events (a live worker row on
// HERDR_PANE_ID) is handed over to Python.

// workerRoles is WORKER_ROLES; TestWorkerRolesMatchPython compares it with the Python literal.
var workerRoles = map[string]bool{
	"pm": true, "coder": true, "qa": true, "reviewer": true, "judge": true, "cleaner": true,
	"staff-engineer": true, "researcher": true, "scout": true, "retro": true, "evaluator": true,
}

// settingsRoleRE is re.search(r"/settings/([\w-]+)\.json$", v); Python's $ also matches before a final newline.
var settingsRoleRE = regexp.MustCompile(`/settings/([\w-]+)\.json\n?\z`)

// ancestryWorker is _ancestry_worker: the worker role named by the first claude ancestor's --settings file, or "".
// startPid is the own pid and startPPid the parent, used when the own pid is not in the process list (or its
// parent is unknown).
func ancestryWorker(procs []gate.Proc, startPid, startPPid int) string {
	byPid := make(map[int]gate.Proc, len(procs))
	for _, p := range procs {
		byPid[p.Pid] = p
	}
	pid := startPid
	if p, ok := byPid[pid]; !ok || p.Ppid < 0 {
		pid = startPPid
	}
	for i := 0; i < 64; i++ {
		cur, known := byPid[pid]
		argv := cur.Argv
		head := argv
		if len(head) > 3 {
			head = head[:3]
		}
		for _, x := range head {
			if x[strings.LastIndex(x, "/")+1:] != "claude" {
				continue
			}
			var vals []string
			for j := 0; j < len(argv)-1; j++ {
				if argv[j] == "--settings" {
					vals = append(vals, argv[j+1])
				}
			}
			for _, x := range argv {
				if strings.HasPrefix(x, "--settings=") {
					vals = append(vals, strings.SplitN(x, "=", 2)[1])
				}
			}
			for _, v := range vals {
				if m := settingsRoleRE.FindStringSubmatch(v); m != nil && workerRoles[m[1]] {
					return m[1]
				}
			}
			return ""
		}
		if !known || cur.Ppid <= 1 {
			return ""
		}
		pid = cur.Ppid
	}
	return ""
}

// hostProcesses is the process list: $FACTORY_PROC_DIR (a fake /proc, off the live home only) or the host's own
// scan. delegate=true is procRoot's: Python must answer. A scan error is returned as err.
func (c *ctx) hostProcesses() (procs []gate.Proc, delegate bool, err error) {
	root, del := c.procRoot()
	if del {
		return nil, true, nil
	}
	if c.env.Getenv("FACTORY_PROC_DIR") != "" {
		procs, err = gate.ReadProcRoot(root)
		return procs, false, err
	}
	procs, err = gate.NewHostFacts(c.env.Getenv("FACTORY_TMP_STATVFS"), runtime.NumCPU()).Processes()
	return procs, false, err
}

// procRoot is proc_root(): /proc, or $FACTORY_PROC_DIR off the live home only. On a live home a set variable makes
// Python record a proc-dir-ignored event, which Go never writes: delegate.
func (c *ctx) procRoot() (string, bool) {
	fake := c.env.Getenv("FACTORY_PROC_DIR")
	if fake == "" {
		return "/proc", false
	}
	live, err := c.env.HomeIsLive()
	if err != nil || live {
		return "", true
	}
	return fake, false
}

// callerIsWorker is caller_is_worker: the worker role of the caller ("" = none). delegate=true means Python must
// answer: a read here would need a write (events) or the answer depends on state Go does not reproduce.
func callerIsWorker(c *ctx) (role string, delegate bool) {
	procs, del, err := c.hostProcesses()
	if del {
		return "", true
	}
	// an unreadable scan leaves no ancestry: fall through to the env and DB checks, as an unreadable /proc did
	if r := ancestryWorker(procs, c.deps.pid(), c.deps.ppid()); r != "" && err == nil {
		return r, false
	}
	if r := c.env.Getenv("FACTORY_ROLE"); workerRoles[r] {
		return r, false
	}
	r, err := actorRole(c)
	if err != nil {
		return "", true
	}
	if workerRoles[r] {
		return r, false
	}
	if pane := c.env.Getenv("HERDR_PANE_ID"); pane != "" {
		rs, err := c.db.Query("SELECT role FROM agents WHERE pane_id=? AND status='live'", pane)
		if err != nil {
			return "", true
		}
		defer rs.Close()
		for rs.Next() {
			var role sql.NullString
			if err := rs.Scan(&role); err != nil {
				return "", true
			}
			if workerRoles[role.String] {
				return "", true // Python's pane path may record identity-spoof / pane-conflict events
			}
		}
		if rs.Err() != nil {
			return "", true
		}
	}
	return "", false
}

// actorRole is actor_role(con): owner, the agents row's role, or "unknown".
func actorRole(c *ctx) (string, error) {
	if c.who == "owner" {
		return "owner", nil
	}
	var role string
	err := c.db.QueryRow("SELECT role FROM agents WHERE name=?", c.who).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "unknown", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// compactView is compact_view: workers get the compact `task show` / `goal show` unless --full. delegate as above.
func compactView(c *ctx, full bool) (compact, delegate bool) {
	if full {
		return false, false // Python short-circuits: no detection runs
	}
	role, del := callerIsWorker(c)
	if del {
		return false, true
	}
	if role != "" {
		return true, false
	}
	r, err := actorRole(c)
	if err != nil {
		return false, true
	}
	return workerRoles[r], false
}
