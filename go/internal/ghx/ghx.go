// Package ghx is the Go port of the gh helpers in bin/factory_cli.py: gh_json, gh_git_env, the judge_ci:/prview: config
// row caches (judge_ci_store, judge_pr_ready, _pr_cache_get/_pr_cache_put) and _gh_red_main_workflow_missing.
// gh is found on PATH (the harness stub included), exactly like subprocess.run(["gh", ...]); never through a shell.
package ghx

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/gitx"
)

// Key prefixes of the config rows; the tick prune (G-041 T-1385) matches the same prefixes.
const (
	JudgeCIPrefix = "judge_ci:"
	PRViewPrefix  = "prview:"
	// JudgeCITTL is JUDGE_CI_TTL_S.
	JudgeCITTL = 120 * time.Second
	// PRViewTTLDefault is PR_VIEW_TTL_S, the default of config pr_view_ttl_s.
	PRViewTTLDefault = 120 * time.Second
)

// Result is gh's stdout, stderr and exit code.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run runs `gh args...` (stdin input, timeout 0 = none). A timeout is *gitx.TimeoutError (Python TimeoutExpired); a
// gh that cannot start is the underlying error (Python OSError).
func Run(env []string, input string, timeout time.Duration, args ...string) (Result, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if ctx.Err() == context.DeadlineExceeded {
		return res, &gitx.TimeoutError{Argv: append([]string{"gh"}, args...), Seconds: timeout.Seconds()}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// JSON is gh_json: runs gh with a 120 s default timeout (0 = that default) and decodes stdout. A non-zero exit returns
// the Python message "gh <first 3 args>: <first stderr line>". Unlike Python it does not print "factory: <msg>" to
// stderr or exit; the caller does that.
func JSON(timeout time.Duration, args ...string) (any, error) {
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	r, err := Run(nil, "", timeout, args...)
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		first := ""
		if lines := strings.Split(strings.TrimSpace(r.Stderr), "\n"); len(lines) > 0 {
			first = strings.TrimSpace(lines[0])
		}
		n := len(args)
		if n > 3 {
			n = 3
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args[:n], " "), first)
	}
	var v any
	if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil {
		return nil, fmt.Errorf("gh %s: bad JSON: %w", strings.Join(args[:min(3, len(args))], " "), err)
	}
	return v, nil
}

// GitEnv is gh_git_env(): the parent env plus the git overrides through GIT_CONFIG_COUNT (command-line scope), since
// gh runs git itself.
func GitEnv(o gitx.Options) []string {
	pairs := o.Overrides()
	env := append([]string{}, os.Environ()...)
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT="+strconv.Itoa(len(pairs)))
	for i, kv := range pairs {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}

// RedMainWorkflowMissing is _gh_red_main_workflow_missing: gh run list says the repo has no tests.yml workflow.
func RedMainWorkflowMissing(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "workflow") && strings.Contains(s, "not found")
}

// pyFloat renders a float like Python's json.dumps does for the "at" value.
func pyFloat(f float64) string {
	if a := math.Abs(f); a < 1e16 && (a >= 1e-4 || a == 0) {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func upsert(db *sql.DB, key, value string) error {
	_, err := db.Exec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func get(db *sql.DB, key string) (string, bool) {
	var v string
	if err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// JudgeCI is the value of a config row judge_ci:<pr_url>: {"at", "ready", "why"}.
type JudgeCI struct {
	At    float64 `json:"at"`
	Ready bool    `json:"ready"`
	Why   string  `json:"why"`
}

// StoreJudgeCI is judge_ci_store: now is epoch seconds. The JSON is Python json.dumps shaped
// ({"at": 1.5, "ready": true, "why": "..."}); non-ASCII is escaped as Python does.
func StoreJudgeCI(db *sql.DB, prURL string, ready bool, why string, now float64) error {
	w, _ := json.Marshal(why)
	return upsert(db, JudgeCIPrefix+prURL, fmt.Sprintf(`{"at": %s, "ready": %t, "why": %s}`, pyFloat(now), ready, asciiEscape(w)))
}

// asciiEscape escapes non-ASCII runes as \uXXXX (json.dumps ensure_ascii) in an already-marshalled JSON string.
func asciiEscape(b []byte) string {
	var sb strings.Builder
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			sb.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&sb, `\u%04x`, r)
		}
	}
	return sb.String()
}

// LoadJudgeCI is the cache read of judge_pr_ready: the row when it is younger than JudgeCITTL (0 <= now-at < ttl),
// else ok=false. An unreadable row is a miss. `ready` may be any JSON truthy value in Python; only bool and 0/1 numbers
// are accepted here, anything else is a miss (one extra gh call, never a wrong merge).
func LoadJudgeCI(db *sql.DB, prURL string, now float64) (JudgeCI, bool) {
	v, ok := get(db, JudgeCIPrefix+prURL)
	if !ok {
		return JudgeCI{}, false
	}
	var raw struct {
		At    *float64 `json:"at"`
		Ready *bool    `json:"ready"`
		Why   *string  `json:"why"`
	}
	if err := json.Unmarshal([]byte(v), &raw); err != nil || raw.At == nil || raw.Ready == nil || raw.Why == nil {
		return JudgeCI{}, false
	}
	age := now - *raw.At
	if math.IsNaN(age) || age < 0 || age >= JudgeCITTL.Seconds() {
		return JudgeCI{}, false
	}
	return JudgeCI{At: *raw.At, Ready: *raw.Ready, Why: *raw.Why}, true
}

// PRViewRow is the value of config prview:<pr_url>: {"at", "head", "pr"}.
type PRViewRow struct {
	At   float64        `json:"at"`
	Head string         `json:"head"`
	PR   map[string]any `json:"pr"`
}

// StorePRView is _pr_cache_put: nothing is stored when ttl <= 0 or pr is nil.
func StorePRView(db *sql.DB, prURL string, pr map[string]any, now float64, ttl time.Duration) error {
	if ttl <= 0 || pr == nil {
		return nil
	}
	head, _ := pr["headRefOid"].(string)
	var h any = head
	if _, ok := pr["headRefOid"].(string); !ok {
		h = nil
	}
	b, err := json.Marshal(map[string]any{"at": now, "head": h, "pr": pr})
	if err != nil {
		return err
	}
	return upsert(db, PRViewPrefix+prURL, asciiEscape(b))
}

// LoadPRView is _pr_cache_get: the cached probe when it is younger than ttl and its head equals head; a nil head (an
// unknown head: ls-remote failed) or a mismatch is a miss.
func LoadPRView(db *sql.DB, prURL string, now float64, head *string, ttl time.Duration) (map[string]any, bool) {
	if ttl <= 0 || head == nil {
		return nil, false
	}
	v, ok := get(db, PRViewPrefix+prURL)
	if !ok {
		return nil, false
	}
	var raw struct {
		At   *float64       `json:"at"`
		Head *string        `json:"head"`
		PR   map[string]any `json:"pr"`
	}
	if err := json.Unmarshal([]byte(v), &raw); err != nil || raw.At == nil || raw.PR == nil || raw.Head == nil {
		return nil, false
	}
	age := now - *raw.At
	if math.IsNaN(age) || age < 0 || age >= ttl.Seconds() || *raw.Head != *head {
		return nil, false
	}
	return raw.PR, true
}
