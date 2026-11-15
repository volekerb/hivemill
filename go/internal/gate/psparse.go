package gate

import (
	"fmt"
	"strings"
	"time"
)

// PsArgs is the one ps call per scan on macOS. lstart is a fixed 24-char ctime field ("Sat Nov  5 20:45:16 2026").
var PsArgs = []string{"-axo", "pid=,ppid=,uid=,lstart=,command="}

const (
	psLstartLen    = 24
	psLstartLayout = "Mon Jan _2 15:04:05 2006"
)

// ParsePs parses the output of `ps -axo pid=,ppid=,uid=,lstart=,command=` run with LC_ALL=C. ps cannot report
// argv boundaries, so Argv is the command split on whitespace. Any line that does not parse fails the whole scan:
// a partial process list would undercount and open the gate. loc is the zone lstart is printed in.
func ParsePs(out string, loc *time.Location) ([]Proc, error) {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p, err := parsePsLine(line, loc)
		if err != nil {
			return nil, err
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func parsePsLine(line string, loc *time.Location) (Proc, error) {
	bad := func(why string) (Proc, error) {
		return Proc{}, fmt.Errorf("ps line %q: %s: %w", line, why, ErrUnparsable)
	}
	rest := line
	var nums [3]int
	for i := range nums {
		rest = strings.TrimLeft(rest, " ")
		j := strings.IndexByte(rest, ' ')
		if j < 0 {
			return bad("too few fields")
		}
		n, err := PyInt(rest[:j])
		if err != nil || n < 0 {
			return bad("number")
		}
		nums[i], rest = n, rest[j:]
	}
	rest = strings.TrimLeft(rest, " ")
	if len(rest) < psLstartLen+1 || rest[psLstartLen] != ' ' {
		return bad("lstart")
	}
	start, err := time.ParseInLocation(psLstartLayout, rest[:psLstartLen], loc)
	if err != nil {
		return bad("lstart")
	}
	return Proc{Pid: nums[0], Ppid: nums[1], Uid: nums[2], Start: start, Argv: strings.Fields(rest[psLstartLen:])}, nil
}
