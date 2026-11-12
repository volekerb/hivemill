// Package blockcond parses typed block-condition tokens for the factory tick.
package blockcond

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Cond is one parsed block condition. Met is filled by evaluation, not parsing.
type Cond struct {
	Type string `json:"type"`
	Arg  string `json:"arg"`
	Form string `json:"form,omitempty"`
	Met  *bool  `json:"met,omitempty"`
}

var (
	reTask  = regexp.MustCompile(`^T-[0-9]+$`)
	reOwner = regexp.MustCompile(`^M[0-9]+$`)
	rePR    = regexp.MustCompile(`^#[0-9]+$`)
	reGoal  = regexp.MustCompile(`^G-[0-9]+$`)
)

var allowedChecks = map[string]bool{
	"gh-worker-ro-token-mode-600": true,
	"gh-worker-rw-token-mode-600": true,
}

func unknown(token string) error {
	return fmt.Errorf("unknown condition: %s", token)
}

// Parse accepts only the closed token grammar documented for G-040 SC1.
func Parse(token string) (Cond, error) {
	var zero Cond
	idx := strings.IndexByte(token, ':')
	if idx <= 0 {
		return zero, unknown(token)
	}
	typ := token[:idx]
	rest := token[idx+1:]
	if rest == "" {
		return zero, unknown(token)
	}

	switch typ {
	case "task":
		if !reTask.MatchString(rest) {
			return zero, unknown(token)
		}
		return Cond{Type: "task", Arg: rest}, nil
	case "pr":
		arg, form, ok := splitForm(rest)
		if !ok || !rePR.MatchString(arg) || (form != "merged" && form != "closed") {
			return zero, unknown(token)
		}
		return Cond{Type: "pr", Arg: arg, Form: form}, nil
	case "file":
		if rest == "" || strings.HasPrefix(rest, "/") || strings.Contains(rest, "..") || strings.Contains(rest, ":") {
			return zero, unknown(token)
		}
		return Cond{Type: "file", Arg: rest}, nil
	case "main":
		if rest != "green" {
			return zero, unknown(token)
		}
		return Cond{Type: "main", Arg: rest}, nil
	case "config":
		if !strings.Contains(rest, "=") {
			return zero, unknown(token)
		}
		return Cond{Type: "config", Arg: rest}, nil
	case "owner":
		if !reOwner.MatchString(rest) {
			return zero, unknown(token)
		}
		return Cond{Type: "owner", Arg: rest}, nil
	case "check":
		if !allowedChecks[rest] {
			return zero, unknown(token)
		}
		return Cond{Type: "check", Arg: rest}, nil
	case "until":
		if _, err := time.Parse(time.RFC3339, rest); err != nil {
			return zero, unknown(token)
		}
		return Cond{Type: "until", Arg: rest}, nil
	case "goal":
		arg, form, ok := splitForm(rest)
		if !ok || !reGoal.MatchString(arg) || (form != "done" && form != "released") {
			return zero, unknown(token)
		}
		return Cond{Type: "goal", Arg: arg, Form: form}, nil
	default:
		return zero, unknown(token)
	}
}

func splitForm(rest string) (arg, form string, ok bool) {
	i := strings.IndexByte(rest, ':')
	if i <= 0 || i >= len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}
