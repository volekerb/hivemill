package fsx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var now = time.Now

// kindDirs maps a log kind to its directory under the state dir.
var kindDirs = map[string]string{"plan": "plans", "apply": "logs"}

// WriteJSON writes v as JSON to <stateDir>/plans/<UTC ts>.json (kind "plan")
// or <stateDir>/logs/<UTC ts>.json (kind "apply"), atomically (temp + rename),
// file mode 0600, directory 0700. It returns the final path.
func WriteJSON(stateDir, kind string, v any) (string, error) {
	sub, ok := kindDirs[kind]
	if !ok {
		return "", fmt.Errorf("fsx: unknown log kind %q (want plan or apply)", kind)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(stateDir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	final := filepath.Join(dir, now().UTC().Format("20060102T150405.000000000Z")+".json")
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return final, nil
}

// PruneLogs deletes *.json regular files older than maxAge, directly in
// <stateDir>/plans and <stateDir>/logs only. Symlinks and subdirectories are
// left alone. It returns the number of files removed.
func PruneLogs(stateDir string, maxAge time.Duration) (int, error) {
	cutoff := now().Add(-maxAge)
	removed := 0
	for _, sub := range []string{"plans", "logs"} {
		dir := filepath.Join(stateDir, sub)
		ents, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		for _, de := range ents {
			if !de.Type().IsRegular() || !strings.HasSuffix(de.Name(), ".json") {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				return removed, err
			}
			if fi.ModTime().Before(cutoff) {
				if err := os.Remove(filepath.Join(dir, de.Name())); err != nil {
					return removed, err
				}
				removed++
			}
		}
	}
	return removed, nil
}

// LogRetention is the default PruneLogs age.
const LogRetention = 30 * 24 * time.Hour
