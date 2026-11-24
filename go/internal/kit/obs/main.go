package obs

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Exit codes, as in ADR 0007 section F.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
	ExitBusy  = 4 // another ingest holds the lock: nothing was done, try again later
)

const usage = `hm-obs: factory observability store (docs/obs.md).

  hm-obs ingest [--home DIR] [--root DIR] [--now RFC3339] [--flow-every DUR]
      Update <home>/state/obs.db from the Claude Code transcripts under --root (default ~/.claude/projects),
      join sessions to agents, tasks, goals and PRs from <home>/db/factory.db, and refresh the pr-flow
      snapshots when the last one is older than --flow-every (default 15m; 0 never). Stores no message text.

  hm-obs api spend [--home DIR] [--hours 24|72|168|720] [--group role|model|goal|project|agent|session]
                   [--role V] [--model V] [--goal V] [--project V] [--agent V] [--session V] [--now RFC3339]
  hm-obs api flow  [--home DIR] [--hours 24|72|168|720] [--project V]
      Print the JSON the web UI's Spend and Flow pages show (/api/obs/spend, /api/obs/flow).

  --home defaults to $FACTORY_HOME. --store FILE puts obs.db elsewhere (factory.db is still read from --home).
`

// Main runs hm-obs with args (without the program name) and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	switch args[0] {
	case "ingest":
		return ingestCmd(args[1:], stdout, stderr)
	case "api":
		if len(args) > 1 && (args[1] == "spend" || args[1] == "flow") {
			return apiCmd(args[1], args[2:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "hm-obs: unknown command %q\n%s", args[0], usage)
	return ExitUsage
}

func homeFlag(fl *flag.FlagSet) (home, store *string) {
	return fl.String("home", os.Getenv("FACTORY_HOME"), ""), fl.String("store", "", "")
}

// pathsOf is the paths of --home, with obs.db and its lock moved to --store when given (factory.db stays read-only).
func pathsOf(home, store string) Paths {
	p := PathsFor(home)
	if store != "" {
		p.Store, p.Lock = store, store+".lock"
	}
	return p
}

func parseNow(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--now %q: want RFC3339 like 2026-11-24T12:00:00Z", s)
	}
	return t.UTC(), nil
}

func ingestCmd(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fl.SetOutput(stderr)
	home, storeF := homeFlag(fl)
	userHome, _ := os.UserHomeDir()
	root := fl.String("root", filepath.Join(userHome, ".claude", "projects"), "")
	nowS := fl.String("now", "", "")
	every := fl.Duration("flow-every", 15*time.Minute, "")
	if fl.Parse(args) != nil || fl.NArg() > 0 || *every < 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	if *home == "" {
		fmt.Fprintln(stderr, "hm-obs: no factory home: pass --home or set FACTORY_HOME")
		return ExitUsage
	}
	now, err := parseNow(*nowS)
	if err != nil {
		fmt.Fprintln(stderr, "hm-obs:", err)
		return ExitUsage
	}
	st, err := Ingest(IngestOptions{Paths: pathsOf(*home, *storeF), Root: *root, Now: now, FlowEvery: *every})
	if errors.Is(err, ErrBusy) {
		fmt.Fprintln(stderr, "hm-obs: another ingest is running; nothing done")
		return ExitBusy
	}
	if err != nil {
		fmt.Fprintln(stderr, "hm-obs:", err)
		return ExitError
	}
	fmt.Fprintf(stdout, "hm-obs ingest: %d files (%d read, %d bytes), %d requests, %d tool calls, %d sessions, %d pruned, flow %s\n",
		st.Files, st.FilesRead, st.BytesRead, st.Requests, st.ToolCalls, st.Sessions, st.Pruned, st.Flow)
	return ExitOK
}

func apiCmd(which string, args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("api "+which, flag.ContinueOnError)
	fl.SetOutput(stderr)
	home, storeF := homeFlag(fl)
	hours := fl.Int("hours", 24, "")
	group := fl.String("group", "role", "")
	nowS := fl.String("now", "", "")
	filters := map[string]*string{}
	for _, g := range Groups {
		filters[g] = fl.String(g, "\x00", "")
	}
	if fl.Parse(args) != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	if *home == "" {
		fmt.Fprintln(stderr, "hm-obs: no factory home: pass --home or set FACTORY_HOME")
		return ExitUsage
	}
	store := pathsOf(*home, *storeF).Store
	var out any
	var err error
	if which == "flow" {
		p := *filters["project"]
		if p == "\x00" {
			p = ""
		}
		out, err = Flow(store, *hours, p)
	} else {
		now, perr := parseNow(*nowS)
		if perr != nil {
			fmt.Fprintln(stderr, "hm-obs:", perr)
			return ExitUsage
		}
		f := map[string]string{}
		for g, v := range filters {
			if *v != "\x00" {
				f[g] = *v
			}
		}
		out, err = Spend(store, SpendQuery{Hours: *hours, Group: *group, Filters: f, Now: now})
	}
	if err != nil {
		fmt.Fprintln(stderr, "hm-obs:", err)
		return ExitError
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(stderr, "hm-obs: write json:", err)
		return ExitError
	}
	return ExitOK
}
