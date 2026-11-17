package gowrap

import "strings"

// bannedFlags are refused as a bare name or as name=value, in any spelling (-x, --x, -test.x, --test.x).
var bannedFlags = map[string]bool{
	"toolexec": true, "exec": true, "vettool": true, "overlay": true, "modfile": true,
	"pgo": true, "pkgdir": true, "linkshared": true, "buildmode": true,
}

// pathFlags take a path that must land inside a writable root. "C" is checked against cwd roots only.
var pathFlags = map[string]bool{
	"o": true, "C": true, "coverprofile": true, "cpuprofile": true, "memprofile": true,
	"blockprofile": true, "mutexprofile": true, "trace": true, "outputdir": true,
}

// flagOf splits an argv element that looks like a flag: -name, --name, -test.name, -name=value.
func flagOf(el string) (name, val string, hasVal, ok bool) {
	if len(el) < 2 || el[0] != '-' || el == "--" {
		return "", "", false, false
	}
	s := strings.TrimPrefix(strings.TrimPrefix(el, "-"), "-")
	s = strings.TrimPrefix(s, "test.")
	name, val, hasVal = strings.Cut(s, "=")
	return name, val, hasVal, true
}

// scanStatic runs the checks that need no filesystem: banned flags, -mod, `@` packages, `go env -w/-u`.
// Every element after the tool is scanned, including flag values and what follows `--` and -args.
func scanStatic(tool, sub string, rest []string) error {
	afterArgs := false
	for i, el := range rest {
		if el == "-args" || el == "--args" {
			afterArgs = true
		}
		name, val, hasVal, ok := flagOf(el)
		if !ok {
			if !afterArgs && strings.Contains(el, "@") && atSubs[sub] {
				return refuse("package argument %q holds @ (module download)", el)
			}
			continue
		}
		if bannedFlags[name] {
			return refuse("flag %s is not allowed", el)
		}
		if name == "mod" {
			if !hasVal {
				if i+1 >= len(rest) {
					return refuse("-mod needs a value (only readonly is allowed)")
				}
				val = rest[i+1]
			}
			if val != "readonly" {
				return refuse("-mod=%s is not allowed (only readonly)", val)
			}
		}
		if tool == "go" && sub == "env" && (name == "w" || name == "u") {
			return refuse("go env -%s is not allowed", name)
		}
	}
	return nil
}

var atSubs = map[string]bool{"build": true, "test": true, "vet": true, "run": true, "list": true}

// scanPaths runs the checks that resolve paths against the roots: output/path flags, gofmt -w, go fmt.
func scanPaths(tool, sub string, rest []string, r *roots) error {
	cwd := r.Cwd
	// go applies -C before anything else, so later relative paths resolve against the -C target.
	for i, el := range rest {
		name, val, hasVal, ok := flagOf(el)
		if !ok || name != "C" {
			continue
		}
		if !hasVal && i+1 < len(rest) {
			val = rest[i+1]
		}
		if rp, err := realPath(r.Cwd, val); err == nil {
			cwd = rp
		}
	}
	gofmtW := false
	var nonFlags []string
	for i, el := range rest {
		name, val, hasVal, ok := flagOf(el)
		if !ok {
			nonFlags = append(nonFlags, el)
			continue
		}
		if tool == "gofmt" && name == "w" {
			gofmtW = true
		}
		if !pathFlags[name] {
			continue
		}
		if !hasVal {
			if i+1 >= len(rest) {
				return refuse("flag %s needs a value", el)
			}
			val = rest[i+1]
		}
		base := cwd
		if name == "C" {
			base = r.Cwd
		}
		rp, err := realPath(base, val)
		if err != nil {
			return refuse("flag %s: cannot resolve %q: %v", el, val, err)
		}
		if name == "C" {
			if !r.cwdOK(rp) {
				return refuse("-C %s is not inside the task worktree or /tmp", val)
			}
		} else if !r.writeOK(rp) {
			return refuse("flag %s: %s is not inside a writable root", el, val)
		}
	}
	if tool == "gofmt" && gofmtW {
		for _, a := range nonFlags {
			rp, err := realPath(cwd, a)
			if err != nil || !r.writeOK(rp) {
				return refuse("gofmt -w: %s is not inside a writable root", a)
			}
		}
	}
	if tool == "go" && sub == "fmt" {
		for _, a := range nonFlags[1:] { // nonFlags[0] is "fmt"
			if !strings.HasPrefix(a, ".") && !strings.HasPrefix(a, "/") {
				return refuse("go fmt: %q must be a relative ./ pattern or an absolute path in a writable root", a)
			}
			p := strings.TrimSuffix(strings.TrimSuffix(a, "..."), "/")
			if p == "" {
				p = "/"
			}
			rp, err := realPath(cwd, p)
			if err != nil || !r.writeOK(rp) {
				return refuse("go fmt: %s is not inside a writable root", a)
			}
		}
	}
	return nil
}
