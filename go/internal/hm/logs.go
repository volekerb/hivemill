package hm

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// RunLogs lists the files under state/hivemill/runs/<task>/ (relative paths, sorted).
func (r *Runner) RunLogs(task string) ([]string, error) {
	dir := filepath.Join(r.Env.StateDir(), "runs", task)
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// RunLog returns one file of a run's log directory, for example round-1/proof-2.log.
func (r *Runner) RunLog(task, file string) (string, error) {
	b, err := os.ReadFile(filepath.Join(r.Env.StateDir(), "runs", task, file))
	return string(b), err
}
