package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Constants shared with scout/nsbox.py.
const (
	RunMask       = "/run"
	NoTokenMsg    = "owner decision D3 pending: no eval token"
	ProxyURL      = "http://127.0.0.1:3128"
	TmpfsSize     = "8m"
	TmpfsMaxBytes = uint64(16) << 30
	SubUID        = 1
	SubGID        = 1
)

var (
	// HideDirs / HideFiles are the deny-mode defaults.
	HideDirs  = []string{"~/.claude", "~/.config", "~/.ssh", "~/.gnupg", "~/factory", "$XDG_RUNTIME_DIR"}
	HideFiles = []string{"~/.git-credentials", "~/.netrc"}
	ScrubEnv  = []string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"}
	NetEnv    = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}
	EnvPass       = []string{"PATH", "LANG", "TERM", "TMPDIR"} // plus LC_*
	ClaudeEnvPass = []string{"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER"}
	EvalEnvPass   = []string{"FACTORY_AGENT"}
	DropCaps      = []string{"--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"}
)

var sizeRE = regexp.MustCompile(`^([0-9]+)([kKmMgG%]?)$`)

// SizeOK reports whether v is a tmpfs size= value: k/m/g bytes with 0 < bytes <= 16g, or N% with 1 <= N <= 100.
// The kernel wraps in 64 bits, so size=0 (and anything that overflows to it) would be an UNLIMITED tmpfs; the
// ceiling refuses those. Other suffixes (t, p, e) are refused.
func SizeOK(v string) bool {
	m := sizeRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return false // overflow: far above the ceiling
	}
	var unit uint64
	switch strings.ToLower(m[2]) {
	case "%":
		return n >= 1 && n <= 100
	case "":
		unit = 1
	case "k":
		unit = 1 << 10
	case "m":
		unit = 1 << 20
	case "g":
		unit = 1 << 30
	}
	if n == 0 || n > TmpfsMaxBytes/unit {
		return false
	}
	return true
}

// RunKeepRO is the dirs to bind back read-only inside the /run mask so DNS keeps working: the parent of
// resolvPath's real path, only when that is a directory strictly under /run. /run itself and /run/user/* never.
func RunKeepRO(resolvPath string) []string {
	real := Realpath(resolvPath)
	parent := filepath.Dir(real)
	if !strings.HasPrefix(real, RunMask+"/") || parent == RunMask {
		return nil
	}
	if parent == RunMask+"/user" || strings.HasPrefix(parent, RunMask+"/user/") {
		return nil
	}
	return []string{parent}
}

// ExpandEntry resolves a leading ~ or $XDG_RUNTIME_DIR of a hide entry.
func ExpandEntry(entry, home, runtime string) string {
	if strings.HasPrefix(entry, "~") || strings.HasPrefix(entry, "$") {
		return strings.ReplaceAll(strings.Replace(entry, "~", home, 1), "$XDG_RUNTIME_DIR", runtime)
	}
	return entry
}

// CopySpec is the subuid-mode private tmpfs copy of a tree.
type CopySpec struct {
	Tree   string   `json:"tree"`
	Size   string   `json:"size"`
	Mkdirs []string `json:"mkdirs"`
}

// Cfg is everything stage 1 needs, as plain JSON with absolute paths. The JSON keys equal the Python cfg's, except
// that python/nsbox/egress (three paths of the Python interpreter) are one key, self: the Go binary that re-execs
// itself as stage 1, the forwarder and the proxy.
type Cfg struct {
	Unshare     string
	Setpriv     string
	Self        string
	UID, GID    int
	Net         bool
	Trial       string
	TrialsRoot  string
	ForwardSock *string
	HideDirs    []string
	HideFiles   []string
	Keep        []string
	KeepRO      []string
	Claude      *string
	Cwd         *string
	Scrub       []string
	EnvSet      map[string]string
	Mode        string // "allow" or "" (deny)
	Rw, Ro      []string
	Masks       []string
	NoPid       bool
	TmpfsSize   string // "" = default
	TmpfsSizes  map[string]string
	Subuid      bool
	Copy        *CopySpec
}

// PidNS reports whether stage 1 gets its own pid namespace (the default).
func (c *Cfg) PidNS() bool { return !c.NoPid }

// MarshalJSON writes the Python cfg shape: the optional keys appear only when non-default.
func (c *Cfg) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"unshare": c.Unshare, "setpriv": c.Setpriv, "self": c.Self, "uid": c.UID, "gid": c.GID, "net": c.Net,
		"trial": c.Trial, "trials_root": c.TrialsRoot, "forward_sock": c.ForwardSock,
		"hide_dirs": nonNil(c.HideDirs), "hide_files": nonNil(c.HideFiles), "keep": nonNil(c.Keep),
		"keep_ro": nonNil(c.KeepRO), "claude": c.Claude, "cwd": c.Cwd, "scrub": nonNil(c.Scrub), "env_set": c.EnvSet,
	}
	if c.EnvSet == nil {
		m["env_set"] = map[string]string{}
	}
	if c.Mode == "allow" {
		m["mode"], m["rw"], m["ro"] = "allow", nonNil(c.Rw), nonNil(c.Ro)
	}
	if len(c.Masks) > 0 {
		m["masks"] = c.Masks
	}
	if c.NoPid {
		m["pid"] = false
	}
	if c.TmpfsSize != "" {
		m["tmpfs_size"] = c.TmpfsSize
	}
	if len(c.TmpfsSizes) > 0 {
		m["tmpfs_sizes"] = c.TmpfsSizes
	}
	if c.Subuid {
		m["subuid"] = true
	}
	if c.Copy != nil {
		m["copy"] = map[string]any{"tree": c.Copy.Tree, "size": c.Copy.Size, "mkdirs": nonNil(c.Copy.Mkdirs)}
	}
	return json.Marshal(m)
}

type cfgWire struct {
	Unshare     string            `json:"unshare"`
	Setpriv     string            `json:"setpriv"`
	Self        string            `json:"self"`
	UID         int               `json:"uid"`
	GID         int               `json:"gid"`
	Net         bool              `json:"net"`
	Trial       string            `json:"trial"`
	TrialsRoot  string            `json:"trials_root"`
	ForwardSock *string           `json:"forward_sock"`
	HideDirs    []string          `json:"hide_dirs"`
	HideFiles   []string          `json:"hide_files"`
	Keep        []string          `json:"keep"`
	KeepRO      []string          `json:"keep_ro"`
	Claude      *string           `json:"claude"`
	Cwd         *string           `json:"cwd"`
	Scrub       []string          `json:"scrub"`
	EnvSet      map[string]string `json:"env_set"`
	Mode        string            `json:"mode"`
	Rw          []string          `json:"rw"`
	Ro          []string          `json:"ro"`
	Masks       []string          `json:"masks"`
	Pid         *bool             `json:"pid"`
	TmpfsSize   string            `json:"tmpfs_size"`
	TmpfsSizes  map[string]string `json:"tmpfs_sizes"`
	Subuid      bool              `json:"subuid"`
	Copy        *CopySpec         `json:"copy"`
}

// UnmarshalJSON reads the shape MarshalJSON writes (and the Python one, bar the three interpreter paths).
func (c *Cfg) UnmarshalJSON(b []byte) error {
	var w cfgWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*c = Cfg{Unshare: w.Unshare, Setpriv: w.Setpriv, Self: w.Self, UID: w.UID, GID: w.GID, Net: w.Net, Trial: w.Trial,
		TrialsRoot: w.TrialsRoot, ForwardSock: w.ForwardSock, HideDirs: w.HideDirs, HideFiles: w.HideFiles,
		Keep: w.Keep, KeepRO: w.KeepRO, Claude: w.Claude, Cwd: w.Cwd, Scrub: w.Scrub, EnvSet: w.EnvSet, Mode: w.Mode,
		Rw: w.Rw, Ro: w.Ro, Masks: w.Masks, NoPid: w.Pid != nil && !*w.Pid, TmpfsSize: w.TmpfsSize,
		TmpfsSizes: w.TmpfsSizes, Subuid: w.Subuid, Copy: w.Copy}
	return nil
}

// Opts are the make_cfg arguments. NewOpts gives the Python defaults; the Seams fields exist for tests and for HQ
// (which passes none of them).
type Opts struct {
	TrialDir, RealClaude, Home, TrialsRoot string
	HideDirs, HideFiles                    []string
	VendorDir                              string
	Keep                                   []string
	Net, Forward                           bool
	Unshare                                string
	Cwd                                    string
	Mode                                   string // "deny" (default) or "allow"
	Rw, Ro, Masks                          []string
	NoPid                                  bool
	KeepEnv                                []string
	TmpfsSize                              string
	TmpfsSizes                             map[string]string
	NoRunMask                              bool
	Subuid                                 bool
	Copy                                   *CopySpec

	// Seams
	UID, GID   int
	Runtime    string // $XDG_RUNTIME_DIR; "" = the env or /run/user/<uid>
	Self       string // "" = this executable
	Setpriv    string
	ResolvConf string // "" = /etc/resolv.conf
}

// NewOpts is the Python default argument set for a trial dir.
func NewOpts(trialDir string) Opts {
	return Opts{TrialDir: trialDir, HideDirs: append([]string{}, HideDirs...), HideFiles: append([]string{}, HideFiles...),
		Mode: "deny", TmpfsSize: TmpfsSize, Unshare: "unshare", UID: os.Getuid(), GID: os.Getgid()}
}

func which(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func ptr(s string) *string { return &s }

// MakeCfg is nsbox.make_cfg: validation and the cfg, with every refusal a plain error carrying Python's message.
func MakeCfg(o Opts) (*Cfg, error) {
	if o.TmpfsSize == "" {
		o.TmpfsSize = TmpfsSize
	}
	if o.Mode == "" {
		o.Mode = "deny"
	}
	bad := !SizeOK(o.TmpfsSize)
	for _, v := range o.TmpfsSizes {
		bad = bad || !SizeOK(v)
	}
	if bad {
		return nil, fmt.Errorf("tmpfs size must be a positive number like 8m, 512k or 1g (0 means unlimited), at most 16g or 100%%, got %q / %v", o.TmpfsSize, o.TmpfsSizes)
	}
	if o.Mode != "deny" && o.Mode != "allow" {
		return nil, fmt.Errorf("mode must be 'deny' or 'allow', got %q", o.Mode)
	}
	home := o.Home
	if home == "" {
		home = RealHome()
	}
	trial := Abs(o.TrialDir)
	runtime := o.Runtime
	if runtime == "" {
		runtime = os.Getenv("XDG_RUNTIME_DIR")
	}
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", o.UID)
	}
	var claude *string
	if o.RealClaude != "" {
		claude = ptr(Abs(o.RealClaude))
	}
	keeps := append([]string{trial}, o.Keep...)
	if claude != nil {
		keeps = append(keeps, *claude, Realpath(*claude))
	}
	self := o.Self
	if self == "" {
		self, _ = os.Executable()
	}
	setpriv := o.Setpriv
	if setpriv == "" {
		setpriv = which("setpriv")
	}
	trialsRoot := filepath.Dir(trial)
	if o.TrialsRoot != "" {
		trialsRoot = Abs(o.TrialsRoot)
	}
	cfg := &Cfg{Unshare: which(o.Unshare), Setpriv: setpriv, Self: self, UID: o.UID, GID: o.GID, Net: o.Net,
		Trial: trial, TrialsRoot: trialsRoot, Claude: claude, EnvSet: map[string]string{}}
	if o.Forward {
		cfg.ForwardSock = ptr(filepath.Join(trial, "proxy.sock"))
		cfg.EnvSet = map[string]string{"HTTPS_PROXY": ProxyURL, "https_proxy": ProxyURL, "NO_PROXY": "", "no_proxy": ""}
	}
	for _, d := range o.HideDirs {
		cfg.HideDirs = append(cfg.HideDirs, ExpandEntry(d, home, runtime))
	}
	for _, f := range o.HideFiles {
		cfg.HideFiles = append(cfg.HideFiles, ExpandEntry(f, home, runtime))
	}
	for _, k := range keeps {
		cfg.Keep = append(cfg.Keep, Abs(k))
	}
	if o.VendorDir != "" {
		cfg.KeepRO = []string{Abs(o.VendorDir)}
	}
	if o.Cwd != "" {
		cfg.Cwd = ptr(Abs(o.Cwd))
	}
	scrub := append([]string{}, ScrubEnv...)
	if !o.Forward {
		scrub = append(scrub, NetEnv...)
	}
	for _, k := range scrub {
		if !contains(o.KeepEnv, k) {
			cfg.Scrub = append(cfg.Scrub, k)
		}
	}
	if o.NoPid && o.Forward {
		return nil, errors.New("pid=False cannot be combined with forward=True: the egress forwarder would outlive the run; use pid=True, or net=True without forward")
	}
	if o.Mode == "allow" {
		if o.VendorDir != "" || len(o.Keep) > 0 {
			return nil, errors.New("vendor_dir and keep are deny-mode args and would be silently dropped in allow mode; list the paths in ro/rw instead")
		}
		for _, p := range append(append([]string{}, o.Rw...), o.Ro...) {
			if isLink(p) {
				return nil, fmt.Errorf("rw/ro path is a symlink: %s; list its realpath %s instead", p, Realpath(p))
			}
			if b := symlinkParent(Abs(p)); b != "" {
				return nil, fmt.Errorf("rw/ro path %s has a symlink parent %s; list its realpath %s instead", p, b, Realpath(p))
			}
		}
		cfg.Mode = "allow"
		cfg.Rw = []string{}
		for _, p := range o.Rw {
			cfg.Rw = append(cfg.Rw, Abs(p))
		}
		cfg.Ro = []string{}
		for _, p := range o.Ro {
			cfg.Ro = append(cfg.Ro, Abs(p))
		}
		if claude != nil {
			cfg.Ro = append(cfg.Ro, Abs(*claude), Abs(Realpath(*claude)))
		}
		if !o.NoRunMask {
			if !contains(cfg.HideDirs, RunMask) {
				cfg.HideDirs = append(cfg.HideDirs, RunMask)
			}
			resolv := o.ResolvConf
			if resolv == "" {
				resolv = "/etc/resolv.conf"
			}
			for _, k := range RunKeepRO(resolv) {
				if isDir(k) && !contains(cfg.Ro, k) {
					cfg.Ro = append(cfg.Ro, k)
				}
			}
		}
		for _, p := range cfg.Ro[len(o.Ro):] {
			if b := symlinkParent(p); b != "" {
				return nil, fmt.Errorf("claude path %s has a symlink parent %s", p, b)
			}
		}
		cfg.Keep, cfg.KeepRO = []string{}, []string{}
	} else if len(o.Rw) > 0 || len(o.Ro) > 0 || len(o.Masks) > 0 {
		return nil, errors.New("rw, ro and masks need mode='allow'")
	}
	if o.Mode != "allow" && (o.TmpfsSize != TmpfsSize || len(o.TmpfsSizes) > 0) {
		return nil, errors.New("tmpfs_size and tmpfs_sizes need mode='allow'; deny mode keeps size=" + TmpfsSize)
	}
	if o.TmpfsSize != TmpfsSize {
		cfg.TmpfsSize = o.TmpfsSize
	}
	if len(o.TmpfsSizes) > 0 {
		cfg.TmpfsSizes = map[string]string{}
		for k, v := range o.TmpfsSizes {
			cfg.TmpfsSizes[Abs(k)] = v
		}
	}
	for _, m := range o.Masks {
		cfg.Masks = append(cfg.Masks, Abs(m))
	}
	cfg.NoPid = o.NoPid
	if o.Subuid {
		if o.Mode != "allow" {
			return nil, errors.New("subuid needs mode='allow'")
		}
		cfg.Subuid = true
		if o.Copy != nil {
			cp := &CopySpec{Tree: Abs(o.Copy.Tree), Size: o.Copy.Size, Mkdirs: []string{}}
			for _, d := range o.Copy.Mkdirs {
				cp.Mkdirs = append(cp.Mkdirs, Abs(d))
			}
			if !SizeOK(cp.Size) {
				return nil, fmt.Errorf("copy size must be like 512m, got %q", o.Copy.Size)
			}
			cfg.Copy = cp
		}
	} else if o.Copy != nil {
		return nil, errors.New("copy needs subuid=True")
	}
	return cfg, nil
}

// ComposeStage2 is the final argv: real uid back, then every capability dropped. The target is claude, or args.
func ComposeStage2(c *Cfg, args []string) []string {
	var cmd []string
	if c.Claude != nil {
		cmd = append([]string{*c.Claude}, args...)
	} else {
		cmd = append([]string{}, args...)
	}
	if c.Subuid {
		out := []string{c.Setpriv, fmt.Sprintf("--reuid=%d", SubUID), fmt.Sprintf("--regid=%d", SubGID), "--clear-groups"}
		return append(append(out, DropCaps...), cmd...)
	}
	out := []string{c.Unshare, fmt.Sprintf("--map-user=%d", c.UID), fmt.Sprintf("--map-group=%d", c.GID), "--keep-caps", c.Setpriv}
	return append(append(out, DropCaps...), cmd...)
}

// ComposeStage1 is the namespace wrapper argv. Stage 1 is this binary's `--setup` (or `--launch` in subuid mode).
func ComposeStage1(c *Cfg, args []string) ([]string, error) {
	js, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if c.Subuid {
		return append([]string{c.Self, "--launch", string(js), "--"}, args...), nil
	}
	out := []string{c.Unshare, "-r"}
	if !c.Net {
		out = append(out, "-n")
	}
	out = append(out, "-m")
	if c.PidNS() {
		out = append(out, "--pid", "--fork", "--kill-child", "--mount-proc")
	}
	out = append(out, "--propagation", "private", c.Self, "--setup", string(js), "--")
	return append(out, args...), nil
}

// RealLeaf is realpath of the parent plus the last name, so a symlinked leaf (the claude link) stays a link.
func RealLeaf(path string) string {
	return filepath.Join(Realpath(filepath.Dir(path)), filepath.Base(path))
}
