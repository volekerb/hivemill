//go:build darwin

package procx

// CwdInside is Fail if any visible process has its cwd at or under dir.
func CwdInside(dir string) Result { return cwdInsideLsof(defaultRunner, dir) }

// HeldOpen is Fail if any visible process has a file open at or under path.
func HeldOpen(path string) Result { return heldOpenLsof(defaultRunner, path) }

// Processes lists every process via ps and lsof.
func Processes() ([]Process, error) { return processesPS(defaultRunner) }
