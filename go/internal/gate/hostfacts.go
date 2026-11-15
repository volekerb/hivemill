package gate

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// TmpMinFreeMB is the macOS /tmp free-space floor (not tmpfs only). A constant on purpose: no config key yet.
const TmpMinFreeMB = 10240

// Memory pressure levels of kern.memorystatus_vm_pressure_level.
const (
	PressureNormal  = 1
	PressureWarn    = 2
	PressureCrit    = 4
	PressureUnknown = -1
)

// PressureName is the word for a level.
func PressureName(l int) string {
	switch l {
	case PressureNormal:
		return "normal"
	case PressureWarn:
		return "warn"
	case PressureCrit:
		return "critical"
	}
	return "unknown"
}

// HostFacts is what the gate and `factory res` read from the host. An error means "could not read or parse".
type HostFacts interface {
	Load1() (float64, error)
	NumCPU() int
	MemAvailableMB() (int64, error)
	SwapUsedMB() (int64, error)
	TmpUsage(dir string) *Tmp // nil when unreadable
	MemPressure() (int, error)
	TmpFloorMB() int64
	// Strict hosts close the gate on an unreadable value; the Linux host keeps its historical delegate-to-Python path.
	Strict() bool
}

// ApplyHostFacts fills the host part of s. On a strict host a value that cannot be read closes the gate: no memory,
// infinite load, unknown pressure, unknown tmp. The returned errors say which readers failed (for `factory res`).
func ApplyHostFacts(s *Snapshot, h HostFacts, tmpDir string) []error {
	var errs []error
	s.NumCPU = h.NumCPU()
	if m, err := h.MemAvailableMB(); err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
		s.MemMB = 0
	} else {
		s.MemMB = m
	}
	if l, err := h.Load1(); err != nil {
		errs = append(errs, fmt.Errorf("load: %w", err))
		s.Load1 = math.Inf(1)
	} else {
		s.Load1 = l
	}
	if p, err := h.MemPressure(); err != nil {
		errs = append(errs, fmt.Errorf("pressure: %w", err))
		s.MemPressure = PressureUnknown
	} else {
		s.MemPressure = p
	}
	s.Tmp = h.TmpUsage(tmpDir)
	s.TmpMinFreeMB = h.TmpFloorMB()
	s.StrictTmp = h.Strict()
	if s.Tmp == nil && h.Strict() {
		errs = append(errs, fmt.Errorf("tmp: unreadable"))
	}
	return errs
}

// Runner runs a command and returns its stdout; tests feed recorded text.
type Runner func(name string, args ...string) (string, error)

var (
	loadBraceRE = regexp.MustCompile(`^\{?\s*([0-9]+(\.[0-9]+)?)\s`)
	swapUsedRE  = regexp.MustCompile(`used = ([0-9]+(\.[0-9]+)?)([MGK])`)
	pageSizeRE  = regexp.MustCompile(`page size of ([0-9]+) bytes`)
	vmLineRE    = regexp.MustCompile(`^Pages (free|inactive|speculative):\s+([0-9]+)\.?\s*$`)
)

func unparsable(what, text string) error {
	return fmt.Errorf("%s %q: %w", what, strings.TrimSpace(text), ErrUnparsable)
}

// ParseDarwinLoad is the first number of `sysctl -n vm.loadavg`: "{ 1.23 1.10 0.98 }".
func ParseDarwinLoad(out string) (float64, error) {
	m := loadBraceRE.FindStringSubmatch(strings.TrimSpace(out) + " ")
	if m == nil {
		return 0, unparsable("vm.loadavg", out)
	}
	return PyFloat(m[1])
}

// ParseDarwinVMStat is (free + inactive + speculative) pages x page size of `vm_stat`, in MB. All three counters and
// the page size must be present.
func ParseDarwinVMStat(out string) (int64, error) {
	var page int64
	got := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		if m := pageSizeRE.FindStringSubmatch(line); m != nil {
			n, err := PyInt(m[1])
			if err != nil {
				return 0, unparsable("vm_stat header", line)
			}
			page = int64(n)
			continue
		}
		if m := vmLineRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, err := PyInt(m[2])
			if err != nil {
				return 0, unparsable("vm_stat line", line)
			}
			got[m[1]] = int64(n)
		}
	}
	if page <= 0 || len(got) != 3 {
		return 0, unparsable("vm_stat", out)
	}
	return (got["free"] + got["inactive"] + got["speculative"]) * page / 1048576, nil
}

// ParseDarwinSwap is the "used = N.NNM" figure of `sysctl -n vm.swapusage`, in MB.
func ParseDarwinSwap(out string) (int64, error) {
	m := swapUsedRE.FindStringSubmatch(out)
	if m == nil {
		return 0, unparsable("vm.swapusage", out)
	}
	v, err := PyFloat(m[1])
	if err != nil {
		return 0, err
	}
	switch m[3] {
	case "G":
		v *= 1024
	case "K":
		v /= 1024
	}
	return int64(v), nil
}

// ParseDarwinPressure is `sysctl -n kern.memorystatus_vm_pressure_level`: 1, 2 or 4.
func ParseDarwinPressure(out string) (int, error) {
	n, err := PyInt(out)
	if err != nil || (n != PressureNormal && n != PressureWarn && n != PressureCrit) {
		return 0, unparsable("kern.memorystatus_vm_pressure_level", out)
	}
	return n, nil
}

// TmpFromStatfs is the darwin tmp reading from Statfs numbers. nil when the filesystem has no size.
func TmpFromStatfs(total, free int64, fstype, mount string) *Tmp {
	if total <= 0 || free < 0 || free > total {
		return nil
	}
	used := total - free
	return &Tmp{UsedMB: FloorDiv(used, 1048576), TotalMB: FloorDiv(total, 1048576), Pct: 100.0 * float64(used) / float64(total),
		FSType: fstype, FreeMB: FloorDiv(free, 1048576), Mount: mount}
}

// DarwinFacts reads macOS through Run and Statfs; both are injected.
type DarwinFacts struct {
	Run    Runner
	CPUs   int
	Statfs func(dir string) (total, free int64, fstype, mount string, err error)
}

func (d DarwinFacts) Strict() bool { return true }

func (d DarwinFacts) NumCPU() int {
	if d.CPUs < 1 {
		return 1
	}
	return d.CPUs
}

func (d DarwinFacts) Load1() (float64, error) {
	out, err := d.Run("sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, err
	}
	return ParseDarwinLoad(out)
}

func (d DarwinFacts) MemAvailableMB() (int64, error) {
	out, err := d.Run("vm_stat")
	if err != nil {
		return 0, err
	}
	return ParseDarwinVMStat(out)
}

func (d DarwinFacts) SwapUsedMB() (int64, error) {
	out, err := d.Run("sysctl", "-n", "vm.swapusage")
	if err != nil {
		return 0, err
	}
	return ParseDarwinSwap(out)
}

func (d DarwinFacts) MemPressure() (int, error) {
	out, err := d.Run("sysctl", "-n", "kern.memorystatus_vm_pressure_level")
	if err != nil {
		return PressureUnknown, err
	}
	return ParseDarwinPressure(out)
}

func (d DarwinFacts) TmpUsage(dir string) *Tmp {
	total, free, fstype, mount, err := d.Statfs(dir)
	if err != nil {
		return nil
	}
	return TmpFromStatfs(total, free, fstype, mount)
}

func (d DarwinFacts) TmpFloorMB() int64 { return TmpMinFreeMB }
