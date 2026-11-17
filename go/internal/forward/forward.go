// Package forward runs a factory CLI call on the HQ machine over ssh (G-057 SC1, plan decision D1): a worker on
// a second host runs the CLI there, the client side sends the call to the HQ host and the server side runs
// the real bin/factory there against the single factory DB. Nothing references this package from live code
// until the owner installs it (G-057 R3). The protocol is in docs/multi-machine.md.
//
// Wire format, client -> server on the ssh channel's stdin:
//
//	"FWD1" | uint32 big-endian length N | N bytes of JSON {"argv":[...],"env":{...},"cwd":"..."} | the call's stdin
//
// Server -> client: the server first writes Ack on stdout, then the CLI's stdout and stderr pass unchanged and
// the ssh exit status is the CLI's exit code. The client strips Ack; it uses it to tell an ssh failure (no Ack,
// exit 255) from a CLI that itself exited 255.
package forward

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// Magic starts every header; a version bump changes it.
	Magic = "FWD1"
	// Ack is the first thing the server writes on stdout.
	Ack = "FWD1-OK\n"
	// MaxHeader bounds the JSON header (bytes after the 8-byte prefix). Larger headers are refused, unread.
	MaxHeader = 64 << 10
	// ForwardedEnv is set to "1" by the server in the CLI's env. A client can never send it.
	ForwardedEnv = "FACTORY_FORWARDED"
)

// AllowedEnv is the exact env allow-list a client may send. Every other key is dropped by the client and refused
// by the server.
var AllowedEnv = []string{
	"FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_GOAL", "FACTORY_TASK",
	"FACTORY_ORG", "FACTORY_CLI_IMPL", "FACTORY_CLI_TRACE",
}

// serverBaseEnv are the keys the server copies from its own (HQ-side) env into the CLI's env so bin/factory can
// run (find python3, its home, its DB). Nothing here comes from the client.
//
// Locale: only LANG, LC_ALL and LC_CTYPE are kept, and they are the HQ side's own, as the ssh session gave them
// (its login shell and sshd). The client's locale is never forwarded and the client cannot send LANG or LC_*
// (they are not in AllowedEnv, so a header carrying them is refused). Other LC_* keys (LC_MESSAGES, ...) are
// dropped. A client's LANG/LC_* can still reach the server's own env through ssh SendEnv/AcceptEnv (if sshd
// accepts them); the server then copies LANG, LC_ALL and LC_CTYPE from that env, so do not rely on it: set the
// locale in the HQ user's shell env.
var serverBaseEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR", "FACTORY_HOME",
}

// Header is one forwarded call.
type Header struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
	Cwd  string            `json:"cwd"`
}

// Refusal is an error the caller turns into exit 2 with its message on stderr.
type Refusal struct{ Msg string }

func (r *Refusal) Error() string { return r.Msg }

func refuse(format string, a ...any) error {
	return &Refusal{Msg: "forward: " + fmt.Sprintf(format, a...)}
}

func allowed(key string) bool {
	for _, k := range AllowedEnv {
		if k == key {
			return true
		}
	}
	return false
}

// ClientEnv picks the allow-listed keys out of environ ("K=V" entries; the last entry for a key wins, as in a
// process env).
func ClientEnv(environ []string) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && allowed(k) {
			out[k] = v
		}
	}
	return out
}

// Validate checks a header: allow-listed env keys only, an absolute cwd, valid UTF-8 and no NUL byte anywhere
// (JSON would silently replace invalid UTF-8; exec cannot pass a NUL).
func (h Header) Validate() error {
	keys := make([]string, 0, len(h.Env))
	for k := range h.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed(k) {
			return refuse("env key not allowed: %s", printable(k))
		}
		if err := textOK("env value of "+k, h.Env[k]); err != nil {
			return err
		}
	}
	for i, a := range h.Argv {
		if err := textOK(fmt.Sprintf("argument %d", i+1), a); err != nil {
			return err
		}
	}
	if err := textOK("cwd", h.Cwd); err != nil {
		return err
	}
	if !filepath.IsAbs(h.Cwd) {
		return refuse("cwd must be an absolute path, got %q", h.Cwd)
	}
	return nil
}

// textOK refuses invalid UTF-8 and NUL. The value itself is never printed.
func textOK(what, s string) error {
	if !utf8.ValidString(s) {
		return refuse("%s is not valid UTF-8", what)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return refuse("%s contains a NUL byte", what)
	}
	return nil
}

// printable keeps a refused key readable in one stderr line.
func printable(s string) string {
	if len(s) > 64 {
		s = s[:64] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// EncodeHeader validates h and returns the wire bytes (prefix + JSON).
func EncodeHeader(h Header) ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	if h.Argv == nil {
		h.Argv = []string{}
	}
	if h.Env == nil {
		h.Env = map[string]string{}
	}
	body, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("forward: encode header: %w", err)
	}
	if len(body) > MaxHeader {
		return nil, refuse("header too large: %d bytes (max %d); pass long text on stdin with '-'", len(body), MaxHeader)
	}
	out := make([]byte, 8, 8+len(body))
	copy(out, Magic)
	binary.BigEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...), nil
}

// ReadHeader reads exactly one header from r and nothing more, so the rest of r is the call's stdin. A header
// above MaxHeader is refused before its body is read.
func ReadHeader(r io.Reader) (Header, error) {
	var h Header
	var pre [8]byte
	if _, err := io.ReadFull(r, pre[:]); err != nil {
		return h, refuse("reading header: %v", err)
	}
	if string(pre[:4]) != Magic {
		return h, refuse("bad header magic (client and server versions differ?)")
	}
	n := binary.BigEndian.Uint32(pre[4:])
	if n > MaxHeader {
		return h, refuse("header too large: %d bytes (max %d)", n, MaxHeader)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return h, refuse("reading header: %v", err)
	}
	if !utf8.Valid(body) {
		return h, refuse("header is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return Header{}, refuse("bad header JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Header{}, refuse("bad header JSON: trailing data")
	}
	if err := h.Validate(); err != nil {
		return Header{}, err
	}
	return h, nil
}

// MapCwd maps a client cwd onto this machine with path_map ([from, to] pairs; the longest matching from wins).
// A cwd outside every mapping maps to home.
func MapCwd(cwd string, pathMap [][]string, home string) string {
	cwd = filepath.Clean(cwd)
	best, bestLen := "", -1
	for _, m := range pathMap {
		from, to := filepath.Clean(m[0]), filepath.Clean(m[1])
		var rest string
		switch {
		case cwd == from:
			rest = ""
		case from == "/":
			rest = strings.TrimPrefix(cwd, "/")
		case strings.HasPrefix(cwd, from+"/"):
			rest = cwd[len(from)+1:]
		default:
			continue
		}
		if len(from) > bestLen {
			best, bestLen = filepath.Join(to, rest), len(from)
		}
	}
	if bestLen < 0 {
		return home
	}
	return best
}

// ChildEnv is the CLI's env on the server: the server's own base keys, the header's allow-listed keys and
// FACTORY_FORWARDED=1. Sorted, so the result is deterministic.
func ChildEnv(h Header, serverEnviron []string) []string {
	m := map[string]string{}
	for _, kv := range serverEnviron {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, b := range serverBaseEnv {
			if k == b {
				m[k] = v
			}
		}
	}
	for k, v := range h.Env {
		if allowed(k) {
			m[k] = v
		}
	}
	m[ForwardedEnv] = "1"
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func lookup(environ []string, key string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}
