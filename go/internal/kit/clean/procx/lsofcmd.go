package procx

import (
	"fmt"
	"os"
	"strings"
)

// cwdInsideLsof is the macOS CwdInside: `lsof -a -d cwd -F pn`.
func cwdInsideLsof(run runner, dir string) Result {
	stdout, stderr, exit, err := run(lsofPath, "-a", "-d", "cwd", "-F", "pn")
	if err != nil {
		return unknown("lsof failed: " + err.Error())
	}
	if exit != 0 && len(stdout) == 0 && !onlyLsofWarnings(stderr) {
		return unknown(fmt.Sprintf("lsof exit %d: %s", exit, strings.TrimSpace(string(stderr))))
	}
	recs, err := parseLsofF(stdout)
	if err != nil {
		return unknown(err.Error())
	}
	roots := candidates(dir)
	for _, r := range recs {
		if under(r[1], roots) {
			return fail(fmt.Sprintf("pid %s cwd %s", r[0], r[1]))
		}
	}
	if exit != 0 && exit != 1 {
		return unknown(fmt.Sprintf("lsof exit %d with output; cannot rule out unread processes", exit))
	}
	if exit == 1 && len(stderr) > 0 && !onlyLsofWarnings(stderr) {
		return unknown("lsof exit 1: " + strings.TrimSpace(string(stderr)))
	}
	return pass()
}

// heldOpenLsof is the macOS HeldOpen: +D for directories, plain path for files.
func heldOpenLsof(run runner, path string) Result {
	args := []string{"-F", "pn"}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		args = append(args, "+D", path)
	} else if err == nil {
		args = append(args, "--", path)
	} else {
		return unknown("stat " + path + ": " + err.Error())
	}
	stdout, stderr, exit, err := run(lsofPath, args...)
	if err != nil {
		return unknown("lsof failed: " + err.Error())
	}
	recs, perr := parseLsofF(stdout)
	if perr != nil {
		return unknown(perr.Error())
	}
	if len(recs) > 0 {
		return fail(fmt.Sprintf("pid %s holds %s", recs[0][0], recs[0][1]))
	}
	if exit == 0 || (exit == 1 && onlyLsofWarnings(stderr)) {
		return pass()
	}
	return unknown(fmt.Sprintf("lsof exit %d: %s", exit, strings.TrimSpace(string(stderr))))
}

// onlyLsofWarnings is true when stderr is empty or all "lsof: WARNING" lines.
func onlyLsofWarnings(stderr []byte) bool {
	for _, l := range strings.Split(string(stderr), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "lsof: WARNING") {
			return false
		}
	}
	return true
}

// processesPS is the macOS Processes: ps for the table and args, lsof for cwd.
func processesPS(run runner) ([]Process, error) {
	out, stderr, exit, err := run(psPath, "-axo", "pid=,ppid=,uid=,lstart=,comm=")
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("ps failed (exit %d): %v %s", exit, err, strings.TrimSpace(string(stderr)))
	}
	ps, err := parsePS(out)
	if err != nil {
		return nil, err
	}
	out, stderr, exit, err = run(psPath, "-axo", "pid=,args=")
	if err != nil || exit != 0 {
		return nil, fmt.Errorf("ps args failed (exit %d): %v %s", exit, err, strings.TrimSpace(string(stderr)))
	}
	args, err := parsePSArgs(out)
	if err != nil {
		return nil, err
	}
	cwds := map[string]string{}
	// cwd is best effort: a missing cwd leaves it empty, it is not needed for ancestry.
	if o, _, _, e := run(lsofPath, "-a", "-d", "cwd", "-F", "pn"); e == nil {
		if recs, e := parseLsofF(o); e == nil {
			for _, r := range recs {
				cwds[r[0]] = r[1]
			}
		}
	}
	for i := range ps {
		ps[i].Argv = args[ps[i].PID]
		ps[i].Cwd = cwds[fmt.Sprint(ps[i].PID)]
	}
	return ps, nil
}
