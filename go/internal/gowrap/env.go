package gowrap

import (
	"path/filepath"
	"sort"
	"strings"
)

// dropped reports whether a caller env var is removed before the pinned values are set.
func dropped(name string) bool {
	switch name {
	case "CC", "CXX", "FC", "AR", "GCCGO", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "BASH_ENV", "ENV":
		return true
	}
	for _, p := range []string{"GO", "CGO_", "PKG_CONFIG"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// buildEnv drops the dangerous caller vars and pins the Go env. The second result is true when the caller had
// a non-empty GOFLAGS (which is ignored).
func buildEnv(caller []string, home string) ([]string, bool) {
	pinned := map[string]string{
		"HOME":        home,
		"PATH":        filepath.Join(home, ".local/go/bin") + ":/usr/bin:/bin",
		"GOTOOLCHAIN": "local",
		"GOENV":       "off",
		"GOWORK":      "off",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
		"GOFLAGS":     "-mod=readonly",
		"CGO_ENABLED": "0",
		"GOMODCACHE":  filepath.Join(home, ".cache/go/path/pkg/mod"),
		"GOCACHE":     filepath.Join(home, ".cache/go/worker-build"),
		"GOTMPDIR":    filepath.Join(home, ".cache/go/tmp"),
	}
	ignored := false
	var out []string
	for _, kv := range caller {
		name, val, _ := strings.Cut(kv, "=")
		if name == "GOFLAGS" && val != "" {
			ignored = true
		}
		if dropped(name) {
			continue
		}
		if _, ok := pinned[name]; ok {
			continue
		}
		out = append(out, kv)
	}
	names := make([]string, 0, len(pinned))
	for k := range pinned {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, k+"="+pinned[k])
	}
	return out, ignored
}
