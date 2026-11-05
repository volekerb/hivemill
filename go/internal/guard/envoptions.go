package guard

import "strings"

// T-0826/T-0964/T-1037: ports worker-guard.py env_long and env_options.

var (
	envLongOK = []string{
		"--null", "--debug", "--block-signal", "--default-signal", "--ignore-signal",
		"--list-signal-handling", "--help", "--version",
	}
	envValueLong = []string{"--argv0", "--chdir"}
)

const envSplitMsg = "env -S/--split-string: workers may not use it; write the command without env -S (T-1037)"

func envLong(a string) string {
	name := a
	if i := strings.IndexByte(a, '='); i >= 0 {
		name = a[:i]
	}
	if len(name) < 3 {
		return "unknown"
	}
	if strings.HasPrefix("--split-string", name) {
		return "split"
	}
	if strings.HasPrefix("--unset", name) {
		return "unset"
	}
	for _, v := range envValueLong {
		if strings.HasPrefix(v, name) {
			return "value"
		}
	}
	for _, v := range envLongOK {
		if name == v {
			return "ok"
		}
	}
	if strings.HasPrefix("--ignore-environment", name) {
		return "ignore"
	}
	return "unknown"
}

type parsedEnvOpt struct {
	kind  string
	value string
}

func envOptions(words []string) []parsedEnvOpt {
	q := append([]string(nil), words...)
	var out []parsedEnvOpt
	val := func() string {
		if len(q) == 0 || strings.HasPrefix(q[0], "-") {
			return ""
		}
		w := q[0]
		q = q[1:]
		return w
	}
	uval := func() string {
		if len(q) == 0 {
			return ""
		}
		if strings.HasPrefix(q[0], "-") {
			return q[0] // peek only; a dash word is never consumed as a value (T-0964)
		}
		return val()
	}
	for len(q) > 0 {
		a := q[0]
		q = q[1:]
		if a == "--" || IsAssign(a) {
			continue
		}
		if a == "-" {
			out = append(out, parsedEnvOpt{"clear", a})
			continue
		}
		if strings.HasPrefix(a, "--") {
			switch envLong(a) {
			case "split":
				out = append(out, parsedEnvOpt{"split", a})
				return out
			case "ignore":
				out = append(out, parsedEnvOpt{"clear", a})
			case "unknown":
				out = append(out, parsedEnvOpt{"unknown", a})
			case "unset":
				v := a
				if i := strings.IndexByte(a, '='); i >= 0 {
					v = a[i+1:]
				} else {
					v = uval()
				}
				out = append(out, parsedEnvOpt{"unset", v})
			case "value":
				if !strings.Contains(a, "=") {
					val()
				}
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			beforeU, _, _ := strings.Cut(a[1:], "u")
			if strings.ContainsRune(beforeU, 'S') {
				out = append(out, parsedEnvOpt{"split", a})
				return out
			}
			scan := beforeU
			if strings.ContainsRune(scan, 'i') {
				out = append(out, parsedEnvOpt{"clear", a})
			} else if strings.ContainsRune(a[1:], 'i') {
				out = append(out, parsedEnvOpt{"clear-attached", a})
			}
			for k, c := range a[1:] {
				if c == 'u' {
					n := a[1+k+1:]
					if n == "" {
						n = uval()
					}
					out = append(out, parsedEnvOpt{"unset", n})
					break
				}
				if c == 'a' || c == 'C' || c == 'P' {
					if k == len(a[1:])-1 {
						val()
					}
					continue
				}
			}
			continue
		}
		return out
	}
	return out
}
