package procx

import (
	"fmt"
	"strconv"
	"strings"
)

// parseLsofF parses `lsof -F pn` output into (pid, name) pairs. A "p" line
// starts a process; each following "n" line is one name for it.
func parseLsofF(out []byte) ([][2]string, error) {
	var res [][2]string
	pid := ""
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if _, err := strconv.Atoi(line[1:]); err != nil {
				return nil, fmt.Errorf("bad lsof pid line %q", line)
			}
			pid = line[1:]
		case 'n':
			if pid == "" {
				return nil, fmt.Errorf("lsof name line before pid: %q", line)
			}
			res = append(res, [2]string{pid, line[1:]})
		default:
			// other field letters are not requested; ignore.
		}
	}
	return res, nil
}

// parsePS parses `ps -axo pid=,ppid=,uid=,lstart=,comm=` output. lstart is five
// tokens ("Mon Jan  2 15:04:05 2006"); comm is the rest of the line.
func parsePS(out []byte) ([]Process, error) {
	var ps []Process
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 9 {
			return nil, fmt.Errorf("short ps line %q", line)
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		uid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, fmt.Errorf("bad ps line %q", line)
		}
		ps = append(ps, Process{
			PID: pid, PPID: ppid, UID: uid,
			Start: strings.Join(f[3:8], " "),
			Comm:  strings.Join(f[8:], " "),
		})
	}
	return ps, nil
}

// parsePSArgs parses `ps -axo pid=,args=`. Arguments are split on whitespace,
// so an argument containing spaces is split (lossy, but argv0 and substring
// matching are unaffected).
func parsePSArgs(out []byte) (map[int][]string, error) {
	m := map[int][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("bad ps args line %q", line)
		}
		m[pid] = f[1:]
	}
	return m, nil
}
