package forward

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func wire(body string) []byte {
	out := make([]byte, 8, 8+len(body))
	copy(out, Magic)
	binary.BigEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

func TestForwardHeaderRoundTrip(t *testing.T) {
	h := Header{Argv: []string{"msg", "x", "a; rm -rf /", ""}, Env: map[string]string{"FACTORY_AGENT": "a"}, Cwd: "/w"}
	b, err := EncodeHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(append(b, "STDIN"...))
	got, err := ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Argv, h.Argv) || got.Env["FACTORY_AGENT"] != "a" || got.Cwd != "/w" {
		t.Fatalf("round trip: %+v", got)
	}
	rest := new(bytes.Buffer)
	if _, err := rest.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if rest.String() != "STDIN" {
		t.Fatalf("ReadHeader consumed stdin: rest %q", rest.String())
	}
}

func TestForwardReadHeaderRefusals(t *testing.T) {
	big := wire(`{"argv":["` + strings.Repeat("a", MaxHeader) + `"],"env":{},"cwd":"/"}`)
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, "reading header"},
		{"short prefix", []byte("FWD1\x00"), "reading header"},
		{"bad magic", append([]byte("XXXX"), wire(`{}`)[4:]...), "bad header magic"},
		{"too large", big, "header too large"},
		{"huge length, no body", []byte("FWD1\xff\xff\xff\xff"), "header too large"},
		{"truncated body", wire(`{"argv":[]}`)[:12], "reading header"},
		{"not json", wire(`nope`), "bad header JSON"},
		{"unknown field", wire(`{"argv":[],"env":{},"cwd":"/","shell":"sh"}`), "bad header JSON"},
		{"trailing data", wire(`{"argv":[],"env":{},"cwd":"/"} {}`), "trailing data"},
		{"bad key", wire(`{"argv":[],"env":{"GH_TOKEN":"v"},"cwd":"/"}`), "env key not allowed: GH_TOKEN"},
		{"forwarded key", wire(`{"argv":[],"env":{"FACTORY_FORWARDED":"1"},"cwd":"/"}`), "env key not allowed: FACTORY_FORWARDED"},
		{"relative cwd", wire(`{"argv":[],"env":{},"cwd":"w"}`), "absolute"},
		{"nul in argv", wire(`{"argv":["a\u0000b"],"env":{},"cwd":"/"}`), "NUL"},
		{"invalid utf-8", wire("{\"argv\":[\"\xff\"],\"env\":{},\"cwd\":\"/\"}"), "UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadHeader(bytes.NewReader(c.in))
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want a refusal containing %q", err, c.want)
			}
		})
	}
}

func TestForwardHeaderLimitIsInclusive(t *testing.T) {
	pad := MaxHeader - len(`{"argv":[""],"env":{},"cwd":"/"}`)
	body := `{"argv":["` + strings.Repeat("a", pad) + `"],"env":{},"cwd":"/"}`
	if len(body) != MaxHeader {
		t.Fatalf("fixture is %d bytes", len(body))
	}
	if _, err := ReadHeader(bytes.NewReader(wire(body))); err != nil {
		t.Fatalf("a header of exactly %d bytes: %v", MaxHeader, err)
	}
	if _, err := EncodeHeader(Header{Argv: []string{strings.Repeat("a", MaxHeader)}, Cwd: "/"}); err == nil {
		t.Fatal("EncodeHeader accepted an oversized header")
	}
}

func TestForwardClientEnvAllowList(t *testing.T) {
	env := ClientEnv([]string{"FACTORY_AGENT=a", "FACTORY_AGENT=b", "GH_TOKEN=x", "HOME=/h", "FACTORY_FORWARDED=1",
		"FACTORY_ROLE=coder", "FACTORY_CLI_TRACE=", "factory_agent=lower", "NOEQUALS"})
	want := map[string]string{"FACTORY_AGENT": "b", "FACTORY_ROLE": "coder", "FACTORY_CLI_TRACE": ""}
	if len(env) != len(want) {
		t.Fatalf("ClientEnv = %v, want %v", env, want)
	}
	for k, v := range want {
		if got, ok := env[k]; !ok || got != v {
			t.Fatalf("ClientEnv[%s] = %q, want %q (all: %v)", k, got, v, env)
		}
	}
}

func TestForwardChildEnv(t *testing.T) {
	h := Header{Env: map[string]string{"FACTORY_AGENT": "a"}}
	got := ChildEnv(h, []string{"PATH=/bin", "HOME=/hq", "GH_TOKEN=x", "FACTORY_AGENT=hq", "FACTORY_FORWARDED=0", "SSH_CONNECTION=1"})
	want := []string{"FACTORY_AGENT=a", "FACTORY_FORWARDED=1", "HOME=/hq", "PATH=/bin"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildEnv = %q, want %q", got, want)
	}
}

func TestForwardChildEnvLocale(t *testing.T) {
	h := Header{Env: map[string]string{"FACTORY_AGENT": "a"}}
	server := []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "LC_CTYPE=C.UTF-8", "LC_MESSAGES=de_DE", "LC_TIME=de_DE"}
	got := ChildEnv(h, server)
	want := []string{"FACTORY_AGENT=a", "FACTORY_FORWARDED=1", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "LC_CTYPE=C.UTF-8"}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildEnv = %q, want %q (server locale kept, other LC_* dropped)", got, want)
	}
	if env := ClientEnv([]string{"LANG=ru_RU.UTF-8", "LC_ALL=ru_RU.UTF-8", "LC_CTYPE=x"}); len(env) != 0 {
		t.Fatalf("ClientEnv forwarded locale keys: %v", env)
	}
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES"} {
		_, err := ReadHeader(bytes.NewReader(wire(`{"argv":["x"],"env":{"` + k + `":"v"},"cwd":"/"}`)))
		if err == nil || !strings.Contains(err.Error(), "env key not allowed: "+k) {
			t.Fatalf("header with %s: err = %v, want env key not allowed", k, err)
		}
	}
}

func TestForwardMapCwd(t *testing.T) {
	pm := [][]string{{"/home/user/factory", "/srv/f"}, {"/home/user/factory/worktrees/deep", "/other"}, {"/data/", "/d"}}
	cases := []struct{ cwd, want string }{
		{"/home/user/factory/worktrees/X", "/srv/f/worktrees/X"},
		{"/home/user/factory", "/srv/f"},
		{"/home/user/factory/", "/srv/f"},
		{"/home/user/factoryX", "/hqhome"},
		{"/home/user/factory/worktrees/deep/a", "/other/a"},
		{"/home/user/factory/../other", "/hqhome"},
		{"/data/x", "/d/x"},
		{"/elsewhere", "/hqhome"},
	}
	for _, c := range cases {
		if got := MapCwd(c.cwd, pm, "/hqhome"); got != c.want {
			t.Errorf("MapCwd(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
	if got := MapCwd("/a/b", [][]string{{"/", "/r"}}, "/h"); got != "/r/a/b" {
		t.Errorf("root mapping: %q", got)
	}
}

func TestForwardConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.json")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := LoadConfig(write(`{"hq_host":"hqhost","remote_receiver":"/opt/hivemill/factory-forward","path_map":[["/a","/b"]]}`))
	if err != nil || c.ValidateClient() != nil {
		t.Fatalf("valid config: %v / %v", err, c.ValidateClient())
	}
	for name, body := range map[string]string{
		"unknown key":   `{"hq_host":"hqhost","x":1}`,
		"three-element": `{"path_map":[["/a","/b","/c"]]}`,
		"relative path": `{"path_map":[["a","/b"]]}`,
		"not json":      `{`,
	} {
		if _, err := LoadConfig(write(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Errorf("missing file: %v", err)
	}
	for _, c := range []Config{
		{HQHost: "hqhost"},
		{HQHost: "FactoryBox", RemoteReceiver: "/x"},
		{HQHost: "u@my-factory", RemoteReceiver: "/x"},
		{HQHost: "-oProxyCommand=id", RemoteReceiver: "/x"},
		{HQHost: "dev box", RemoteReceiver: "/x"},
		{HQHost: "hqhost", RemoteReceiver: "/x y"},
		{HQHost: "hqhost", RemoteReceiver: "-x"},
		{HQHost: "hqhost", RemoteReceiver: "/x;id"},
	} {
		if c.ValidateClient() == nil {
			t.Errorf("ValidateClient accepted %+v", c)
		}
	}
	if (Config{HQHost: "u@workerhost", RemoteReceiver: "~/bin/factory-forward"}).ValidateClient() != nil {
		t.Error("ValidateClient refused user@host and a ~ path")
	}
	if (Config{FactoryBin: "bin/factory"}).ValidateServer() == nil {
		t.Error("ValidateServer accepted a relative factory_bin")
	}
}

func TestForwardConfigPath(t *testing.T) {
	if p, _ := ConfigPath([]string{"HOME=/h", "FORWARD_CONFIG=/x.json"}); p != "/x.json" {
		t.Errorf("override: %q", p)
	}
	if p, _ := ConfigPath([]string{"HOME=/h"}); p != "/h/.config/hivemill/forward.json" {
		t.Errorf("default: %q", p)
	}
	if _, err := ConfigPath(nil); err == nil {
		t.Error("no HOME: accepted")
	}
}

func TestForwardStdinKinds(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	reg, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		f    *os.File
		want bool
	}{{"pipe", pr, true}, {"regular", reg, true}, {"devnull", devnull, false}, {"closed", closed, false}, {"nil", nil, false}} {
		if got := ForwardStdin(c.f); got != c.want {
			t.Errorf("%s: ForwardStdin = %v, want %v", c.name, got, c.want)
		}
	}
}

func FuzzReadHeader(f *testing.F) {
	f.Add(wire(`{"argv":["inbox"],"env":{"FACTORY_AGENT":"a"},"cwd":"/w"}`))
	f.Add(wire(`{"argv":[],"env":{"GH_TOKEN":"v"},"cwd":"/"}`))
	f.Add([]byte("FWD1\xff\xff\xff\xff"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		h, err := ReadHeader(bytes.NewReader(in))
		if err != nil {
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("non-refusal error %T: %v", err, err)
			}
			return
		}
		if h.Validate() != nil {
			t.Fatal("ReadHeader returned an invalid header")
		}
		for k := range h.Env {
			if !allowed(k) {
				t.Fatalf("key %q passed", k)
			}
		}
		b, err := EncodeHeader(h)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if _, err := ReadHeader(bytes.NewReader(b)); err != nil {
			t.Fatalf("re-read: %v", err)
		}
	})
}
