//go:build linux

package gate

import "syscall"

// statfsBytes is the total and available bytes of the filesystem holding dir (statvfs: f_blocks, f_bavail x f_frsize).
func statfsBytes(dir string) (total, free int64, err error) {
	var v syscall.Statfs_t
	if err := syscall.Statfs(dir, &v); err != nil {
		return 0, 0, err
	}
	return int64(v.Blocks) * int64(v.Frsize), int64(v.Bavail) * int64(v.Frsize), nil
}

// LinuxFacts reads /proc and /sys, exactly as `factory res` and the gate did before HostFacts existed.
type LinuxFacts struct {
	MeminfoPath, LoadavgPath, CPUOnlinePath, MountsPath string
	Fallback                                            int    // NumCPU when CPUOnlinePath is unreadable
	FakeStatvfs                                         string // $FACTORY_TMP_STATVFS
}

// NewHostFacts returns the readers of this OS.
func NewHostFacts(fakeStatvfs string, fallbackCPU int) HostFacts {
	return LinuxFacts{"/proc/meminfo", "/proc/loadavg", "/sys/devices/system/cpu/online", "/proc/mounts", fallbackCPU, fakeStatvfs}
}

func (LinuxFacts) Strict() bool { return false }

func (f LinuxFacts) Load1() (float64, error) {
	s, err := ReadFile(f.LoadavgPath)
	if err != nil {
		return 0, err
	}
	return Load1(s)
}

func (f LinuxFacts) NumCPU() int { return CPUCount(f.CPUOnlinePath, f.Fallback) }

func (f LinuxFacts) MemAvailableMB() (int64, error) {
	s, err := ReadFile(f.MeminfoPath)
	if err != nil {
		return 0, err
	}
	return MemAvailableMB(s)
}

func (f LinuxFacts) SwapUsedMB() (int64, error) {
	s, err := ReadFile(f.MeminfoPath)
	if err != nil {
		return 0, err
	}
	return SwapUsedMB(s)
}

func (f LinuxFacts) TmpUsage(dir string) *Tmp { return TmpUsage(dir, f.FakeStatvfs, f.MountsPath) }

// MemPressure is not reported on Linux: 0 leaves the gate as it was.
func (LinuxFacts) MemPressure() (int, error) { return 0, nil }

// TmpFloorMB is 0 on Linux: no floor.
func (LinuxFacts) TmpFloorMB() int64 { return 0 }
