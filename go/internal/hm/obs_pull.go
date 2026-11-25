package hm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// `hivemill obs pull --from HOST:PATH` copies another machine's event log, read-only, into <state>/obs/<label>/.
// The copy replaces the previous one (never appends), so pulling twice is the same as pulling once.

// DefaultRemoteLog is the path used when --from names only a host ("host:"): relative to the remote home.
const DefaultRemoteLog = "factory/state/hivemill/events.jsonl"

// Fetcher reads one file from a host.
type Fetcher func(host, path string) ([]byte, error)

type pullMeta struct {
	From   string `json:"from"`
	Pulled string `json:"pulled"`
	Lines  int    `json:"lines"`
}

var labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ParsePullFrom splits HOST:PATH. A spec without a colon, or one that starts with / . or ~, is a local file (a
// mounted share): host is then "".
func ParsePullFrom(spec string) (host, path string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("--from is empty")
	}
	if strings.HasPrefix(spec, "-") {
		return "", "", fmt.Errorf("--from %q: starts with '-'", spec)
	}
	h, p, ok := strings.Cut(spec, ":")
	if !ok || strings.ContainsAny(spec[:1], "/.~") {
		return "", spec, nil
	}
	if h == "" || strings.ContainsAny(h, " \t'\"`$;&|<>") {
		return "", "", fmt.Errorf("--from %q: bad host", spec)
	}
	if p == "" {
		p = DefaultRemoteLog
	}
	return h, p, nil
}

// PullLabel is the default label of a remote source: the host without user@ (a local file has none: --as).
func PullLabel(host string) string {
	if i := strings.LastIndex(host, "@"); i >= 0 {
		return host[i+1:]
	}
	return host
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SSHFetch reads a remote file with `ssh -o BatchMode=yes HOST cat -- PATH`: no prompt, nothing written there.
func SSHFetch(host, path string) ([]byte, error) {
	path = strings.TrimPrefix(path, "~/") // ssh runs the command in the remote home
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", host, "cat -- "+shellQuote(path))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %v: %s", host, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ObsPull copies one source into the cache and returns its label and the number of event lines.
func (r *Runner) ObsPull(spec, label string, fetch Fetcher, now time.Time) (string, int, error) {
	host, path, err := ParsePullFrom(spec)
	if err != nil {
		return "", 0, err
	}
	if label == "" && host == "" {
		return "", 0, fmt.Errorf("--from %s is a local file: name its machine with --as NAME", spec)
	}
	if label == "" {
		label = PullLabel(host)
	}
	if !labelRe.MatchString(label) {
		return "", 0, fmt.Errorf("label %q: use letters, digits, '.', '_' or '-' (--as NAME)", label)
	}
	if label == r.Machine() {
		return "", 0, fmt.Errorf("label %q is this machine's name; pick another with --as", label)
	}
	var data []byte
	if host == "" {
		data, err = os.ReadFile(path)
	} else {
		if fetch == nil {
			fetch = SSHFetch
		}
		data, err = fetch(host, path)
	}
	if err != nil {
		return "", 0, err
	}
	n, bad := 0, 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) == nil && e.Event != "" {
			n++
		} else {
			bad++
		}
	}
	if n == 0 && bad > 0 {
		return "", 0, fmt.Errorf("%s: not a hivemill event log (%d unreadable lines)", spec, bad)
	}
	dir := filepath.Join(r.Env.ObsDir(), label)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(dir, ".events.jsonl.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "events.jsonl")); err != nil {
		return "", 0, err
	}
	m, _ := json.Marshal(pullMeta{From: spec, Pulled: now.Format(time.RFC3339), Lines: n})
	return label, n, os.WriteFile(filepath.Join(dir, "meta.json"), m, 0o600)
}
