package hm

// Release channels and per-project drain: one runner serves every project, and a project on the stable channel is run
// by the last promoted binary, so a bad edge build does not stall it. The daemon (edge) skips such a project in its
// own pass and runs `<stable> run --project ID` instead; the sessions of the project call the stable binary from their
// hooks and PATH. Until a binary is promoted, a stable project is run by edge.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	ChannelEdge   = "edge"
	ChannelStable = "stable"
	// promoteAfter is how long the edge build must have run clean before it may become the stable one.
	promoteAfter = 24 * time.Hour
	// stableMargin is the time of a stable project pass beside its longest gate_cmd (gate_timeout_s).
	stableMargin = 30 * time.Minute
	edgeMeta     = "edge_build"
	stableMeta   = "stable_sha" // the checksum of the promoted binary
	// ExitPassSkipped is the exit code of `run --project` when another pass of the project runs.
	ExitPassSkipped = 4
)

// ErrPassSkipped is a project pass that did not run.
var ErrPassSkipped = errors.New("another pass is running")

// channelKey is the project of a `project.<id>.channel` config key.
func channelKey(key string) (string, bool) {
	id, ok := strings.CutPrefix(key, "project.")
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, ".channel")
	return id, ok && id != ""
}

// Channel is the release channel of a project: edge unless its config says stable.
func (r *Runner) Channel(project string) string {
	if r.Store.Cfg("project."+project+".channel") == ChannelStable {
		return ChannelStable
	}
	return ChannelEdge
}

// StableBinary is where `hivemill release promote` puts the stable build.
func StableBinary(userHome string) string {
	return filepath.Join(userHome, ".local", "lib", "hivemill", "stable", "hivemill")
}

// stableFor is the promoted binary of a stable-channel project ("" for edge, when nothing is promoted yet, or when the
// file is not the one that was promoted: the daemon never runs a file something else wrote there).
func (r *Runner) stableFor(project string) string {
	if project == "" || r.Channel(project) != ChannelStable {
		return ""
	}
	bin := StableBinary(r.Env.UserHome)
	if st, err := os.Stat(bin); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return ""
	}
	if want := r.Store.Meta(stableMeta); want == "" || r.stableSum(bin) != want {
		return ""
	}
	return bin
}

// stableSum is the checksum of the promoted binary, read again only when its size or time changed: a pass asks for it
// once per run row.
func (r *Runner) stableSum(bin string) string {
	st, err := os.Stat(bin)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d", bin, st.Size(), st.ModTime().UnixNano())
	if v, ok := r.stableSums.Load(key); ok {
		return v.(string)
	}
	sum, _, err := fileSum(bin)
	if err != nil {
		return ""
	}
	r.stableSums.Store(key, sum)
	return sum
}

// Delegate is the binary that must run a step of the project instead of this process ("": this process runs it).
func (r *Runner) Delegate(project string) string {
	bin := r.stableFor(project)
	if bin == "" {
		return ""
	}
	self, err := SelfBinary()
	if err != nil {
		return "" // cannot tell who we are: run here rather than start a loop
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil && p == self {
		return "" // this process is the stable binary
	}
	return bin
}

// DelegateTask is Delegate for the project of a task's run or task row.
func (r *Runner) DelegateTask(task string) string {
	if run, err := r.Store.Run(task); err == nil && run != nil {
		return r.Delegate(run.Project)
	}
	if t, err := r.Store.Task(task); err == nil {
		return r.Delegate(t.Project)
	}
	return ""
}

// DelegateAgent is Delegate for the run an agent session (task or judge) works for.
func (r *Runner) DelegateAgent(agent string) string {
	if agent == "" {
		return ""
	}
	if run := r.runFor(agent); run != nil {
		return r.Delegate(run.Project)
	}
	return ""
}

// stableFailAlert is the number of failed stable passes in a row after which the owner is told.
const stableFailAlert = 3

// stablePasses starts a pass of every delegated project through the stable binary, each one beside the edge pass, so a
// slow or broken stable pass holds back no other project. A project whose last stable pass still runs is left alone.
// The daemon does not wait for them; waitStable does for a command run by hand.
func (r *Runner) stablePasses(startNew bool) {
	projects, err := r.Store.ProjectIDs()
	if err != nil {
		r.printf("stable channel: %v\n", err)
		return
	}
	for _, id := range projects {
		bin := r.Delegate(id)
		if bin == "" {
			if r.Channel(id) == ChannelEdge {
				r.clearStableFail(id) // moved back to edge: its next stable failures alert again
			}
			continue
		}
		argv := []string{bin, "run", "--project", id}
		if !startNew {
			argv = append(argv, "--no-start")
		}
		if _, running := r.stableRunning.LoadOrStore(id, true); running {
			continue
		}
		// the child is not the daemon: it must not ping the daemon's watchdog socket
		env := append(slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "NOTIFY_SOCKET=") }),
			"FACTORY_HOME="+r.Env.Home)
		limit := r.StablePassLimit()
		r.stableWG.Add(1)
		go func() {
			defer r.stableWG.Done()
			defer r.stableRunning.Delete(id)
			rc, out := r.Tools.Sh("", limit, env, argv...)
			if s := strings.TrimSpace(out); s != "" {
				r.printf("%s\n", s)
			}
			r.stableResult(id, rc)
		}()
	}
}

// StablePassLimit bounds one project pass of the stable binary: a full gate for every run slot, plus stableMargin.
func (r *Runner) StablePassLimit() time.Duration {
	return time.Duration(cfgInt(r.Store, "gate_timeout_s")*max(1, cfgInt(r.Store, "max_concurrent")))*time.Second + stableMargin
}

// clearStableFail forgets the failed stable passes of a project and closes their alert.
func (r *Runner) clearStableFail(project string) {
	if key := "stable_fail:" + project; r.Store.Meta(key) != "" {
		_ = r.Store.SetMeta(key, "")
		_ = r.Store.CloseAlert("hivemill-stable", project)
	}
}

// waitStable waits for the stable passes this process started, pinging the watchdog meanwhile.
func (r *Runner) waitStable() {
	stop := r.keepAlive(time.Minute)
	defer stop()
	r.stableWG.Wait()
}

// stableResult counts the failed stable passes in a row of a project; at stableFailAlert it raises one alert.
func (r *Runner) stableResult(project string, rc int) {
	key := "stable_fail:" + project
	if rc == ExitPassSkipped {
		return // neither good nor bad
	}
	if rc == 0 {
		r.clearStableFail(project)
		return
	}
	r.printf("project %s: stable pass failed: exit %d\n", project, rc)
	n, _ := strconv.Atoi(r.Store.Meta(key))
	n++
	_ = r.Store.SetMeta(key, strconv.Itoa(n))
	if n != stableFailAlert {
		return
	}
	_ = r.Log.Emit("release.stable_failed", nil, Event{Data: map[string]any{"project": project, "exit": rc, "passes": n}})
	_ = r.Store.AlertFeed("hivemill-stable", project, "hivemill run --project "+project)
	if _, err := r.Alert("hivemill stable passes fail", fmt.Sprintf("Project %s: %d stable passes failed in a row (exit %d). "+
		"It does not advance. Run `hivemill run --project %s` to see why.", project, n, rc, project)); err != nil {
		r.printf("alert: %v\n", err)
	}
}

// undrained are the projects of ids that are not in drain mode.
func (r *Runner) undrained(ids []string) []string {
	return slices.DeleteFunc(slices.Clone(ids), r.ProjectDraining)
}

// checkProject refuses a project id the factory does not know.
func (r *Runner) checkProject(id string) error {
	ids, err := r.Store.ProjectIDs()
	if err != nil {
		return err
	}
	for _, x := range ids {
		if x == id {
			return nil
		}
	}
	return fmt.Errorf("%w: no project %q (known: %s)", ErrRefused, id, strings.Join(ids, ", "))
}

// ProjectDraining reports drain mode of one project: no new runs for it, active runs finish.
func (r *Runner) ProjectDraining(project string) bool {
	return project != "" && r.Store.Meta("drain:"+project) == "on"
}

// SetProjectDrain turns drain mode of one project on or off.
func (r *Runner) SetProjectDrain(project string, on bool) error {
	if err := r.checkProject(project); err != nil {
		return err
	}
	v := "off"
	if on {
		v = "on"
	}
	return r.Store.SetMeta("drain:"+project, v)
}

// DrainedProjects are the projects in drain mode.
func (r *Runner) DrainedProjects() []string {
	ids, err := r.Store.ProjectIDs()
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		if r.ProjectDraining(id) {
			out = append(out, id)
		}
	}
	return out
}

// edgeBuild is the daemon's record of the binary it runs and of its clean time.
type edgeBuild struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Since   int64  `json:"since"`    // start of the clean time (0: the last pass failed, or an auto-revert)
	LastEnd int64  `json:"last_end"` // end of the last good pass
	Running bool   `json:"running"`  // a pass started and has not ended: found set at a start, that pass died
}

func (r *Runner) readEdge() (b edgeBuild) {
	_ = json.Unmarshal([]byte(r.Store.Meta(edgeMeta)), &b)
	return b
}

func (r *Runner) writeEdge(b edgeBuild) {
	text, err := json.Marshal(b)
	if err == nil {
		err = r.Store.SetMeta(edgeMeta, string(text))
	}
	if err != nil {
		r.printf("edge build record: %v\n", err)
	}
}

// edgePassStart records that a pass of the build (path, sum) starts. The clean time starts over for another build,
// after a pass that never ended, and after a time with no good pass (the build did not run then).
func (r *Runner) edgePassStart(path, sum string, now time.Time) {
	b := r.readEdge()
	gap := now.Sub(time.Unix(b.LastEnd, 0))
	if b.SHA256 != sum || b.Running || gap > time.Duration(cfgInt(r.Store, "health_max_age_s"))*time.Second {
		b.Since = 0
	}
	if b.Since == 0 {
		b.Since = now.Unix()
	}
	b.Path, b.SHA256, b.Running = path, sum, true
	r.writeEdge(b)
}

// edgePassEnd records the end of that pass; a failed one ends the clean time.
func (r *Runner) edgePassEnd(failed bool, now time.Time) {
	b := r.readEdge()
	b.Running = false
	if failed {
		b.Since = 0
	} else {
		b.LastEnd = now.Unix()
	}
	r.writeEdge(b)
}

// edgePassSkipped records that the pass did not run (another one held the lock): it earns no clean time.
func (r *Runner) edgePassSkipped() {
	b := r.readEdge()
	b.Running = false
	r.writeEdge(b)
}

// edgeDirty ends the clean time (an auto-revert).
func (r *Runner) edgeDirty() {
	if b := r.readEdge(); b.SHA256 != "" {
		b.Since = 0
		r.writeEdge(b)
	}
}

func fileSum(path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

// Promote copies the running edge binary to the stable path, once it has run promoteAfter with no failed pass and no
// auto-revert. It returns the stable path.
func (r *Runner) Promote(now time.Time) (string, error) {
	b := r.readEdge()
	if b.SHA256 == "" {
		return "", fmt.Errorf("%w: no record of a running edge build; `hivemill daemon` writes it", ErrRefused)
	}
	if b.Since == 0 || b.LastEnd < b.Since {
		return "", fmt.Errorf("%w: the edge build had a failed pass or an auto-revert and has no good pass since", ErrRefused)
	}
	if b.Running && !r.DaemonRunning() {
		return "", fmt.Errorf("%w: a pass of the edge build never ended: the daemon died in it", ErrRefused)
	}
	if age := now.Sub(time.Unix(b.LastEnd, 0)); age > time.Duration(cfgInt(r.Store, "health_max_age_s"))*time.Second {
		return "", fmt.Errorf("%w: the last good pass of the edge build ended %s ago; is the daemon running?", ErrRefused, age.Round(time.Second))
	}
	if clean := time.Unix(b.LastEnd, 0).Sub(time.Unix(b.Since, 0)); clean < promoteAfter {
		return "", fmt.Errorf("%w: the edge build has run clean for %s of %s", ErrRefused, clean.Round(time.Minute), promoteAfter)
	}
	sum, data, err := fileSum(b.Path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && sum != b.SHA256) {
		return "", fmt.Errorf("%w: %s changed since the daemon started it; restart the daemon and let the new build run %s",
			ErrRefused, b.Path, promoteAfter)
	}
	if err != nil {
		return "", err
	}
	dst := StableBinary(r.Env.UserHome)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".promote-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // gone after the rename
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o755)
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst) // atomic: a running stable process keeps its own file
	}
	if err == nil {
		err = r.Store.SetMeta(stableMeta, sum)
	}
	if err != nil {
		return "", err
	}
	if ids, err := r.Store.ProjectIDs(); err == nil {
		for _, id := range ids {
			r.clearStableFail(id) // another binary: count anew
		}
	}
	_ = r.Log.Emit("release.promoted", nil, Event{Data: map[string]any{"sha256": sum, "from": b.Path}})
	return dst, nil
}
