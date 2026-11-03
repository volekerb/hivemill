package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The guard export (T-0767): bin/factory writes one plain JSON file every tick with the guard-relevant rows of the
// factory DB, so the binary decides own_goal, git_push_pin, test_sandbox, public_text_repos / frozen_repos and
// scrub_terms_file itself instead of re-running the Python hook. Go opens no sqlite (stdlib only).
//
//	<FACTORY_HOME>/run/guard-export.json   mode 0600, written atomically
//	{"version":1, "generated_at":"<RFC 3339>", "config":{"public_text_repos":..,"frozen_repos":..,
//	 "scrub_terms_file":..,"test_sandbox":..}, "agent_goals":{"<agent>":"<goal id>"}, "task_worktrees":{"T-0001":"<path>"}}
//
// It lives in run/ (git-ignored), not state/: state/ is committed and pushed, and a timestamp that changes every tick
// would add a commit per tick.
//
// Where the DB file does not exist the hook's "no DB" branch is taken without the export (as before). Where the DB
// exists, a decision that needs these values fails closed (exit 2) when the file is missing, unreadable, unparsable,
// of an unknown version, or older than GuardExportMaxAge.

// GuardExportMaxAge is how old generated_at may be. The tick writes the file about once a minute; fifteen minutes
// tolerates a slow or restarting tick and still fails closed when the tick is dead (a coder cannot push, a worker
// cannot read its goal, until `factory tick` runs again).
const GuardExportMaxAge = 15 * time.Minute

// guardExportVersion is the only layout this reader understands.
const guardExportVersion = 1

// guardNow is the clock of the staleness check (tests replace it).
var guardNow = time.Now

type guardExport struct {
	Version     int               `json:"version"`
	GeneratedAt string            `json:"generated_at"`
	Config      map[string]string `json:"config"`
	AgentGoals  map[string]string `json:"agent_goals"`
	Worktrees   map[string]string `json:"task_worktrees"`
}

var cachedExport struct {
	path string
	done bool
	exp  *guardExport
	err  error
}

// guardExportPath is <FACTORY_HOME>/run/guard-export.json; FACTORY_HOME defaults to the live home, as for the DB.
func guardExportPath() string {
	home := os.Getenv("FACTORY_HOME")
	if home == "" {
		home = mustHost().liveHome
	}
	return pyJoin(pyJoin(home, "run"), "guard-export.json")
}

// exportFail is the fail-closed error: the binary exits 2 (never the Python hook, never an allow).
func exportFail(why string) error {
	return &Delegate{Why: why, Closed: true}
}

// loadExport reads and validates the export once per process.
func loadExport() (*guardExport, error) {
	if p := guardExportPath(); !cachedExport.done || cachedExport.path != p {
		cachedExport.exp, cachedExport.err = readExport(p)
		cachedExport.path, cachedExport.done = p, true
	}
	return cachedExport.exp, cachedExport.err
}

func readExport(path string) (*guardExport, error) {
	fix := "run `factory tick` (or ask your lead) to regenerate it"
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, exportFail("the guard export " + path + " does not exist; " + fix)
		}
		return nil, exportFail(fmt.Sprintf("the guard export %s cannot be read (%v); %s", path, err, fix))
	}
	var e guardExport
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, exportFail(fmt.Sprintf("the guard export %s is not valid JSON (%v); %s", path, err, fix))
	}
	if e.Version != guardExportVersion || e.Config == nil || e.AgentGoals == nil || e.Worktrees == nil {
		return nil, exportFail("the guard export " + path + " has an unknown layout; " + fix)
	}
	at, err := time.Parse(time.RFC3339, e.GeneratedAt)
	if err != nil {
		return nil, exportFail("the guard export " + path + " has no valid generated_at; " + fix)
	}
	if age := guardNow().Sub(at); age > GuardExportMaxAge || age < -GuardExportMaxAge {
		return nil, exportFail(fmt.Sprintf("the guard export %s is stale (generated_at %s, limit %s); %s",
			path, e.GeneratedAt, GuardExportMaxAge, fix))
	}
	return &e, nil
}

// exportNeeded loads the export for a decision that reads the DB; (nil, nil) means "no DB file": the hook's own
// no-DB branch applies and the caller treats every value as absent.
func exportNeeded() (*guardExport, error) {
	if dbMissing() {
		return nil, nil
	}
	return loadExport()
}

// ownGoal is a port of Analyzer.own_goal: agents.goal_id of $FACTORY_AGENT, "" for none.
func ownGoal() (string, error) {
	agent := os.Getenv("FACTORY_AGENT")
	if agent == "" {
		return "", nil
	}
	e, err := exportNeeded()
	if e == nil {
		return "", err
	}
	// agent_goals is rewritten by the tick (about once a minute), not by `factory spawn`: an agent spawned since
	// the last tick is absent from the map for up to one tick and reads as "no goal" (own-goal decisions deny, which
	// is the safe side). A stale or missing export fails closed (exit 2, see GuardExportMaxAge).
	return e.AgentGoals[agent], nil
}

// taskWorktree is a port of Analyzer.task_worktree: realpath of tasks.worktree for the task, ok false for none.
func taskWorktree(task string) (string, bool, error) {
	e, err := exportNeeded()
	if e == nil {
		return "", false, err
	}
	w := e.Worktrees[strings.ToUpper(task)]
	if w == "" {
		return "", false, nil
	}
	return pyRealpath(w), true, nil
}

// testSandboxOn is a port of Analyzer.test_sandbox_on: config test_sandbox is 1.
func testSandboxOn() (bool, error) {
	e, err := exportNeeded()
	if e == nil {
		return false, err
	}
	return strings.TrimFunc(e.Config["test_sandbox"], pyIsSpace) == "1", nil
}

// scrubTermsFile is cfg.get("scrub_terms_file") of PublicText: "" for none.
func scrubTermsFile() (string, error) {
	e, err := exportNeeded()
	if e == nil {
		return "", err
	}
	return e.Config["scrub_terms_file"], nil
}
