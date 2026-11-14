package tick

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Config is everything Run touches; tests replace the process-level parts.
type Config struct {
	Home     string
	DB       *sql.DB // used by Go units only
	Stdout   io.Writer
	Stderr   io.Writer
	Environ  []string
	Python   string // python3 path
	Entry    string // <checkout>/bin/factory
	Clock    func() float64
	Getpid   func() int
	TimersOn func() bool // tick_phase_timers, read after the lock is taken
	Go       map[string]GoFunc
}

type segment struct {
	units []string
	goFn  GoFunc
}

// segments groups the walk: consecutive Python units form one child (H11), a Go unit stands alone.
func segments(goFns map[string]GoFunc) []segment {
	var segs []segment
	for _, u := range walkUnits() {
		if fn, ok := goFns[u.Name]; ok {
			segs = append(segs, segment{units: []string{u.Name}, goFn: fn})
			continue
		}
		if n := len(segs); n > 0 && segs[n-1].goFn == nil {
			last := segs[n-1].units[len(segs[n-1].units)-1]
			gap := unitTableIndex(u.Name) - unitTableIndex(last)
			switch {
			case gap == 1:
				segs[n-1].units = append(segs[n-1].units, u.Name)
			case gap == 2 && Units[unitTableIndex(last)+1].Name == OfflineUnit:
				// Python --phases forbids gaps: the offline row rides along (a no-op unless herdr_list gave None).
				segs[n-1].units = append(segs[n-1].units, OfflineUnit, u.Name)
			default:
				segs = append(segs, segment{units: []string{u.Name}})
			}
		} else {
			segs = append(segs, segment{units: []string{u.Name}})
		}
	}
	return segs
}

type driver struct {
	cfg     Config
	t0Lock  float64
	timers  bool
	state   State
	timer   Timer
	t0      float64 // _PT["t0"]: where the total starts; set by the child that ran migrate_db
	t0Set   bool
	emitted bool
	handoff string
}

func (d *driver) fail(code int, format string, a ...any) int {
	fmt.Fprintf(d.cfg.Stderr, "factory(go): "+format+"\n", a...)
	return code
}

// Run takes the lock and runs one tick; it returns the process exit code. Busy lock: the Python line, exit 0.
func Run(cfg Config) int {
	if cfg.Go == nil {
		cfg.Go = goUnits
	}
	if cfg.Getpid == nil {
		cfg.Getpid = os.Getpid
	}
	lock, busy, err := TakeLock(cfg.Home)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "factory(go): %v\n", err)
		return 1
	}
	if busy {
		fmt.Fprintln(cfg.Stdout, "another tick/up is running; skipping")
		return 0
	}
	defer lock.Close()
	d := &driver{cfg: cfg, state: State{}, handoff: filepath.Join(cfg.Home, "run", HandoffName)}
	defer os.Remove(d.handoff)
	d.t0Lock = cfg.Clock()
	d.timer.Last = d.t0Lock
	d.t0 = d.t0Lock
	d.timers = cfg.TimersOn != nil && cfg.TimersOn()
	return d.walk()
}

func (d *driver) walk() int {
	segs := segments(d.cfg.Go)
	for i, sg := range segs {
		last := i == len(segs)-1
		var stop bool
		var code int
		if sg.goFn != nil {
			stop, code = d.runGo(sg.units[0], sg.goFn)
		} else {
			list := sg.units
			if last && d.timers {
				list = append(append([]string(nil), list...), EmitUnit)
				d.emitted = true
			}
			stop, code = d.runPython(list)
		}
		if code >= 0 {
			return code
		}
		if !stop && sg.units[len(sg.units)-1] == pruneUnit && d.state.AgentsNull() {
			// herdr was unreachable: the tick ends with the offline branch (drift, "exporting only", export)
			stop, code = d.runPython([]string{OfflineUnit})
			if code >= 0 {
				return code
			}
			stop = true
			if d.timers {
				d.emitted = true // offline ends with _pt_emit, same as the all-Python tick
			}
		}
		if stop {
			break
		}
	}
	if d.timers && !d.emitted {
		d.emitted = true
		if _, code := d.runPython([]string{EmitUnit}); code >= 0 {
			return code
		}
	}
	return 0
}

// runGo runs one Go unit in-process and laps it. Returns (stop, -1) to go on, or (_, exit code).
func (d *driver) runGo(name string, fn GoFunc) (stop bool, code int) {
	u := unitByName(name)
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("unit %s panicked: %v", name, r)
			}
		}()
		return fn(&UnitCtx{Ctx: context.Background(), DB: d.cfg.DB, Home: d.cfg.Home, State: d.state,
			Stdout: func(s string) { io.WriteString(d.cfg.Stdout, s) }})
	}()
	if err != nil {
		return false, d.fail(1, "%v", err)
	}
	if u.Lap != "" {
		d.timer.Lap(u.Lap, d.cfg.Clock())
	}
	return false, -1
}

func unitByName(n string) Unit {
	for _, u := range Units {
		if u.Name == n {
			return u
		}
	}
	return Unit{Name: n}
}

// runPython starts one child for units. Returns (stop, -1) to go on, or (_, exit code) to end the tick.
func (d *driver) runPython(units []string) (stop bool, code int) {
	now := d.cfg.Clock()
	elapsed, total := now-d.t0Lock, LapsTotal(d.timer.Laps)
	if len(d.timer.Laps) == 0 && len(d.timer.Ctr.Keys()) == 0 {
		elapsed, total = 0, 0 // first child: match the all-Python tick _PT bootstrap (no driver time in handoff)
	}
	h := Handoff{Pid: d.cfg.Getpid(), Elapsed: elapsed, Timers: d.timers, State: d.state,
		Laps: d.timer.Laps, Counters: d.timer.Ctr, Total: total}
	if _, err := WriteHandoff(d.cfg.Home, h); err != nil {
		return false, d.fail(1, "%v", err)
	}
	s := d.cfg.Clock()
	res := runChild(d.cfg.Python, d.cfg.Entry, d.cfg.Environ, d.handoff, units, d.cfg.Stdout, d.cfg.Stderr)
	e := d.cfg.Clock()
	list := phaseSpec(units)
	switch {
	case res.err != nil:
		return false, d.fail(1, "cannot run the tick child (%s): %v; check that python3 and bin/factory exist", list, res.err)
	case res.signal != "":
		return false, d.fail(1, "tick child killed by %s", res.signal)
	case res.code != 0:
		return false, res.code
	}
	out, err := ReadHandoff(d.handoff)
	if err != nil {
		return false, d.fail(1, "tick handoff unreadable after %s", list)
	}
	d.state = out.State
	if out.PreReset != nil && !d.t0Set {
		d.t0, d.t0Set = s+*out.PreReset, true
	}
	if len(d.timer.Laps) == 0 {
		if out.PreReset != nil {
			d.timer.Last = s - *out.PreReset
		} else {
			d.timer.Last = s // driver setup before the first child is not migrate_db time
		}
	}
	d.timer.Child(s, e, out.Laps, out.Tail)
	d.timer.Ctr.Merge(out.Counters)
	return out.Stop, -1
}
