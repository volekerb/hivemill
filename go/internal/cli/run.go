package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// Deps is everything Run touches outside its own memory; tests replace the process-level parts.
type Deps struct {
	Args     []string // os.Args[1:]
	Argv0    string   // os.Args[0]: the bin/factory path the Python dispatcher was started as
	Environ  []string // os.Environ()
	Env      identity.Env
	Stdout   io.Writer
	Stderr   io.Writer
	Exec     ExecFunc                     // nil: syscall.Exec
	LookPath func(string) (string, error) // nil: exec.LookPath
}

// Run executes one factory command and returns its exit code. It never returns when it hands over to Python.
func Run(d Deps) int {
	var out, errb bytes.Buffer
	d.Env.Stderr = &errb
	code, err := d.run(&out, &errb)
	if errors.Is(err, errDelegate) {
		return d.delegate() // buffered output is dropped: Python redoes the whole command
	}
	var ee *identity.ExitError
	if errors.As(err, &ee) {
		fmt.Fprintf(&errb, "factory: %s\n", ee.Msg)
		code = ee.Code
	} else if err != nil {
		fmt.Fprintf(&errb, "factory: %v\n", err)
		code = 1
	}
	d.Stdout.Write(out.Bytes())
	d.Stderr.Write(errb.Bytes())
	return code
}

// command is a parsed argv this package can answer.
type command struct {
	name   string
	all    bool
	json   bool
	key    string
	hasKey bool
	value  *string
	mode   string // config: "", "changed", "check"
}

// parse accepts only the exact shapes of whoami, flags, agents and the config reads; ok=false means "delegate".
func parse(args []string) (command, bool) {
	if len(args) == 0 {
		return command{}, false
	}
	c := command{name: args[0]}
	rest := args[1:]
	switch c.name {
	case "whoami":
		return c, len(rest) == 0
	case "agents":
		for _, a := range rest {
			if a != "--all" {
				return c, false
			}
			c.all = true
		}
		return c, true
	case "flags":
		for _, a := range rest {
			switch a {
			case "--all":
				c.all = true
			case "--json":
				c.json = true
			default:
				return c, false
			}
		}
		return c, true
	case "config":
		var pos []string
		for _, a := range rest {
			switch {
			case a == "--changed" || a == "--check":
				if c.mode != "" {
					return c, false
				}
				c.mode = strings.TrimPrefix(a, "--")
			case strings.HasPrefix(a, "-") || a == "":
				return c, false
			default:
				pos = append(pos, a)
			}
		}
		if len(pos) > 1 || (c.mode != "" && len(pos) > 0) { // two positionals = a write; flags take none
			return c, false
		}
		if len(pos) == 1 {
			c.key, c.hasKey = pos[0], true
		}
		return c, true
	}
	return c, false
}

func (d *Deps) run(out, errb *bytes.Buffer) (int, error) {
	if err := d.Env.EvaluatorHomeGuard(); err != nil {
		return 0, err
	}
	if d.Env.Getenv("FACTORY_CLI_IMPL") == "python" {
		return 0, errDelegate // the owner (or a delegating parent) forced Python: never answer here, never loop
	}
	c, ok := parse(d.Args)
	if !ok {
		return 0, errDelegate
	}
	path := factorydb.ResolvePath(d.Env.Home())
	if _, err := os.Stat(path); err != nil {
		return 0, errDelegate // Python says what to do: `factory init`, or that the file moved
	}
	db, err := factorydb.Open(path) // a user_version mismatch (ErrSchemaMismatch) lands here too
	if err != nil {
		return 0, errDelegate
	}
	defer db.Close()
	who, err := d.Env.Resolve(func() (*factorydb.DB, error) { return db, nil })
	if err != nil {
		return 0, err
	}
	if err := identity.RequireKnown(who, identity.Cmd{Name: c.name, Value: c.value}); err != nil {
		return 0, err
	}
	switch c.name {
	case "whoami":
		return 0, cmdWhoami(db.DB, who, out)
	case "agents":
		return 0, cmdAgents(db.DB, c.all, out)
	case "flags":
		return 0, d.cmdFlags(db.DB, c, out)
	default:
		return d.cmdConfig(db.DB, c, out)
	}
}

func cmdWhoami(db *sql.DB, who string, out io.Writer) error {
	if who == identity.Unknown {
		fmt.Fprintln(out, "unknown (agent pane, identity lost — no privileges; export FACTORY_AGENT=<your agent name>)")
		return nil
	}
	var name, role, status sql.NullString
	var goal, team, task, model sql.NullString
	err := db.QueryRow("SELECT name,role,goal_id,team_id,task_id,model,status FROM agents WHERE name=?", who).
		Scan(&name, &role, &goal, &team, &task, &model, &status)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(out, "%s (owner — not a factory agent)\n", who)
		return nil
	}
	if err != nil {
		return errDelegate
	}
	fmt.Fprintf(out, "%s  role=%s  goal=%s  team=%s  task=%s  model=%s\n",
		name.String, role.String, pyNull(goal), pyNull(team), pyNull(task), pyNull(model))
	return nil
}

// pyNull is str(value) of a nullable text column: None prints as "None".
func pyNull(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return s.String
}

var agentCols = []string{"name", "org", "role", "status", "runtime", "flagless", "relaunch", "goal_id", "team_id", "task_id",
	"pane_id", "model", "started"}

const agentsQuery = "SELECT name,role,status," +
	"COALESCE(org, json_extract(spawn_args,'$.org'), CASE json_extract(spawn_args,'$.runtime') WHEN 'cursor' " +
	"THEN 'cursor' ELSE 'claude' END) AS org," +
	"COALESCE(json_extract(spawn_args,'$.runtime'),'claude') AS runtime," +
	"CASE WHEN flagless THEN 'FLAGLESS' ELSE '' END flagless," +
	"CASE WHEN relaunch_requested_at IS NOT NULL THEN 'RELAUNCH-PENDING' ELSE '' END relaunch," +
	"goal_id,team_id,task_id,pane_id,model,started FROM agents"

func cmdAgents(db *sql.DB, all bool, out io.Writer) error {
	q := agentsQuery
	if !all {
		q += " WHERE status IN ('live','starting','queued')"
	}
	rs, err := db.Query(q + " ORDER BY status, started")
	if err != nil {
		return errDelegate
	}
	defer rs.Close()
	got, err := rs.Columns()
	if err != nil {
		return errDelegate
	}
	idx := map[string]int{}
	for i, n := range got {
		idx[n] = i
	}
	var rows [][]string
	for rs.Next() {
		vals := make([]any, len(got))
		ptrs := make([]any, len(got))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return errDelegate
		}
		row := make([]string, len(agentCols))
		for i, c := range agentCols {
			s, err := cell(vals[idx[c]])
			if err != nil {
				return err
			}
			row[i] = s
		}
		rows = append(rows, row)
	}
	if rs.Err() != nil {
		return errDelegate
	}
	Table(out, agentCols, rows)
	return nil
}

// cell is `"" if v is None else str(v)` for a value read from SQLite.
func cell(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	}
	return "", errDelegate // a float or blob: Python's str() is not reproduced here
}

// cfgValue is cfg(con, key): the stored value, else the default.
type cfgValue struct {
	s    string
	null bool
}

func (v cfgValue) String() string {
	if v.null {
		return "None"
	}
	return v.s
}

func loadConfig(db *sql.DB) (map[string]cfgValue, error) {
	rs, err := db.Query("SELECT key,value FROM config")
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	m := map[string]cfgValue{}
	for rs.Next() {
		var k string
		var v any
		if err := rs.Scan(&k, &v); err != nil {
			return nil, errDelegate
		}
		switch x := v.(type) {
		case nil:
			m[k] = cfgValue{null: true}
		case string:
			m[k] = cfgValue{s: x}
		case []byte:
			m[k] = cfgValue{s: string(x)}
		default:
			return nil, errDelegate // TEXT affinity makes this unreachable; never guess how Python prints it
		}
	}
	return m, rs.Err()
}

func cfgOf(live map[string]cfgValue, defaults map[string]string, key string) cfgValue {
	if v, ok := live[key]; ok {
		return v
	}
	return cfgValue{s: defaults[key]}
}

func (d *Deps) cmdFlags(db *sql.DB, c command, out io.Writer) error {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(d.pyEntry()), "flags.json"))
	if err != nil {
		return errDelegate // Python words the error
	}
	root, err := parseJSON(raw)
	if err != nil || root.kind != 'o' {
		return errDelegate
	}
	keys, ok := root.get("keys")
	if !ok || keys.kind != 'a' {
		return errDelegate
	}
	for _, e := range keys.arr {
		if e.kind != 'o' {
			return errDelegate
		}
		if _, ok := e.get("key"); !ok {
			return errDelegate
		}
		if _, ok := e.get("kind"); !ok {
			return errDelegate
		}
	}
	live, err := loadConfig(db)
	if err != nil {
		return err
	}
	defaults := defaultMap()
	var rows []*jnode
	for _, e := range keys.arr {
		kind, _ := e.get("kind")
		if !(kind.kind == 's' && kind.s == "flag") && !c.all && !c.json {
			continue
		}
		key, _ := e.get("key")
		state, hasState := e.get("state")
		cur := strNode("-")
		if key.kind != 's' {
			return errDelegate
		}
		if _, known := defaults[key.s]; !(hasState && state.kind == 's' && state.s == "removed") && known {
			v := cfgOf(live, defaults, key.s)
			if v.null {
				cur = nullNode()
			} else {
				cur = strNode(v.s)
			}
		}
		r := e.clone()
		r.set("current", cur)
		rows = append(rows, r)
	}
	if c.json {
		fmt.Fprintln(out, dumpIndent1(&jnode{kind: 'a', arr: rows}))
		return nil
	}
	cols := []string{"key", "state", "default", "current", "goal", "doc"}
	var table [][]string
	for _, r := range rows {
		line := make([]string, len(cols))
		for i, col := range cols {
			n, ok := r.get(col)
			if !ok {
				if col != "state" {
					return errDelegate // Python raises KeyError
				}
				n = strNode("-")
			}
			s, err := n.pyStr()
			if err != nil {
				return errDelegate
			}
			line[i] = s
		}
		table = append(table, line)
	}
	Table(out, cols, table)
	return nil
}

// ruleKeys are the config keys whose set path validates the value (_config_value_rules); `config --check` for a
// stored value that differs from the default is left to Python, which owns those validators.
var ruleKeys = map[string]bool{
	"public_text_repos": true, "frozen_repos": true, "issue_intake_repos": true, "noreply_email": true,
	"opus_effort": true, "scout_triage_per_run": true, "model_cleaner": true, "guard_impl": true,
	"sandbox_roles": true, "worker_gh_roles": true, "gh_rw_roles": true, "cursor_pilot_roles": true,
	"agent_runtimes": true, "worker_gh_repos": true, "factory_remote_url": true, "task_clones": true,
	"terminal_backend": true, "skilleval_root": true,
}

func (d *Deps) cmdConfig(db *sql.DB, c command, out io.Writer) (int, error) {
	live, err := loadConfig(db)
	if err != nil {
		return 0, err
	}
	defaults := defaultMap()
	switch {
	case c.mode == "changed":
		n := 0
		for _, e := range DefaultConfig {
			v := cfgOf(live, defaults, e.Key)
			if v.null || v.s != e.Value {
				fmt.Fprintf(out, "%s = %s (default: %s)\n", e.Key, v, e.Value)
				n++
			}
		}
		if n == 0 {
			fmt.Fprintln(out, "no changes from defaults")
		}
		return 0, nil
	case c.mode == "check":
		bad := 0
		for _, e := range DefaultConfig {
			v := cfgOf(live, defaults, e.Key)
			switch {
			case v.null:
				fmt.Fprintf(out, "%s: %s is NULL: fix it with `factory config %s <value>`\n", e.Key, e.Key, e.Key)
				bad++
			case ruleKeys[e.Key] && v.s != e.Value:
				return 0, errDelegate
			}
		}
		if bad > 0 {
			return 2, nil
		}
		return 0, nil
	case c.hasKey:
		if _, ok := defaults[c.key]; !ok {
			known := make([]string, len(DefaultConfig))
			for i, e := range DefaultConfig {
				known[i] = e.Key
			}
			return 0, &identity.ExitError{Code: 2, Msg: fmt.Sprintf("unknown key %s; known: %s", c.key, strings.Join(known, ", "))}
		}
		fmt.Fprintln(out, cfgOf(live, defaults, c.key))
		return 0, nil
	}
	for _, e := range DefaultConfig {
		fmt.Fprintf(out, "%s = %s\n", e.Key, cfgOf(live, defaults, e.Key))
	}
	state, err := d.guardBinaryState()
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(out, "guard binary: %s\n", state)
	return 0, nil
}
