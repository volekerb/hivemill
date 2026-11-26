package install

import (
	"math/big"
	"regexp"
	"strings"
)

// Defaults and pins carried over from install.py.
const (
	DefaultRepoURL = "https://github.com/volekerb/hivemill.git"
	DefaultPort    = 8765
	Loopback       = "127.0.0.1"
)

// Args are the parsed command-line options.
type Args struct {
	Check, DryRun, Yes bool
	Ref, RepoURL       string
	ProjectID          string
	ProjectName        string
	ProjectRepo        string
	ProjectGH          string
	MaxAgents          string // decimal text of a positive integer, "" when not given
	Port               int    // 0 when not given
	NoSystemd, Linger  bool
	// WithServe: macOS only, also install the hivemill.serve launchd job (Linux always installs the serve unit).
	WithServe          bool
	UninstallBootUnits bool
	NoScout, Resume    bool

	HerdrApp  bool
	Factories []string // --factory [LABEL=]URL, once per factory
	NoSwitch  bool
	NoSidebar bool
}

const usageLine = "usage: hivemill-install (--check | --dry-run | --yes) [--ref REF] [--repo-url URL] [--project-id ID --project-repo PATH]\n" +
	"       [--project-name NAME] [--project-gh OWNER/NAME] [--max-agents N] [--port N] [--no-systemd|--no-boot-units] [--with-serve] [--uninstall-boot-units] [--linger]\n" +
	"       [--no-scout] [--resume] [--herdr-app [--factory [LABEL=]URL]... [--no-switch] [--no-sidebar]]"

// helpText is printed for --help.
const helpText = usageLine + `

Non-interactive hivemill installer

mode (exactly one):
  --check              read-only report
  --dry-run            print the ordered plan, change nothing
  --yes                do the work

options:
  --ref REF            tag, branch or commit to install; "latest" (default) picks the newest vX.Y.Z tag
  --repo-url URL       repository to clone (default ` + DefaultRepoURL + `)
  --project-id ID      register one project (needs --project-repo)
  --project-name NAME  name of that project
  --project-repo PATH  local path of that project
  --project-gh OWNER/NAME  GitHub name of that project
  --max-agents N       agent cap (positive integer)
  --port N             port of the web UI (1-65535, default 8765)
  --no-systemd         skip the boot units (systemd user units; launchd agents on macOS)
  --no-boot-units      same as --no-systemd, the neutral name
  --with-serve         macOS: also install the hivemill.serve launchd job (Linux always installs the serve unit)
  --uninstall-boot-units  macOS: boot out hivemill.* and remove their plists (with --dry-run or --yes)
  --linger             also run loginctl enable-linger
  --no-scout           turn the scout off
  --resume             continue after a login it asked for

Herdr app dashboard (--herdr-app with --dry-run or --yes):
  --herdr-app          install the Hivemill dashboard into the Herdr app here
  --factory [LABEL=]URL  a factory's hivemill obs address, e.g. Workshop=http://HOST:7461; repeat it per factory
                       (default http://127.0.0.1:7461)
  --no-switch          do not switch the app to the Hivemill layout
  --no-sidebar         hide the sidebar in the Hivemill layout

Exit codes: 0 done, 1 a step failed or a refusal, 2 usage, 3 you must do something (the command is printed),
4 already installed. The last line of a run is RESULT PASS url=... version=... or RESULT FAIL step=...`

var (
	factoryURLRe = regexp.MustCompile(`^https?://[^` + spaceClass + `=]+\n?$`) // Python: ^https?://[^\s=]+$ ($ also matches before a final \n)
	httpPrefixRe = regexp.MustCompile(`^https?://`)
	hasHelpArg   = map[string]bool{"-h": true, "--help": true}
)

// helpRequested is returned by ParseArgs for -h/--help.
type helpRequested struct{}

func (helpRequested) Error() string { return "help requested" }

// parseFactory splits --factory [LABEL=]URL: the URL is where that factory's `hivemill obs` answers.
func parseFactory(spec string) (label, url string, ok bool) {
	label, url, sep := strings.Cut(spec, "=")
	if !sep || httpPrefixRe.MatchString(spec) {
		label, url = "", spec
	}
	label = strings.TrimSpace(label)
	if !factoryURLRe.MatchString(url) || (sep && label == "" && url != spec) {
		return "", "", false
	}
	return label, strings.TrimRight(url, "/"), true
}

func usageErr(msg string) error { return stopf(2, "usage error: %s\n%s", msg, usageLine) }

// looksNegativeNumber is argparse's test for a value like -5 (accepted as a value, not an option).
var negNumRe = regexp.MustCompile(`^-\p{Nd}+$|^-\p{Nd}*\.\p{Nd}+$`)

// ParseArgs parses argv (without the program name). Rejected values are never echoed: they may be a token.
// A -h/--help argument returns a helpRequested error.
func ParseArgs(argv []string) (*Args, error) {
	a := &Args{Ref: "latest", RepoURL: DefaultRepoURL}
	boolFlags := map[string]*bool{
		"--check": &a.Check, "--dry-run": &a.DryRun, "--yes": &a.Yes, "--no-systemd": &a.NoSystemd, "--no-boot-units": &a.NoSystemd, "--with-serve": &a.WithServe,
		"--uninstall-boot-units": &a.UninstallBootUnits,
		"--linger":               &a.Linger, "--no-scout": &a.NoScout, "--resume": &a.Resume, "--herdr-app": &a.HerdrApp,
		"--no-switch": &a.NoSwitch, "--no-sidebar": &a.NoSidebar,
	}
	valueFlags := map[string]func(string) error{
		"--ref":          func(v string) error { a.Ref = v; return nil },
		"--repo-url":     func(v string) error { a.RepoURL = v; return nil },
		"--project-id":   func(v string) error { a.ProjectID = v; return nil },
		"--project-name": func(v string) error { a.ProjectName = v; return nil },
		"--project-repo": func(v string) error { a.ProjectRepo = v; return nil },
		"--project-gh":   func(v string) error { a.ProjectGH = v; return nil },
		"--factory":      func(v string) error { a.Factories = append(a.Factories, v); return nil },
		"--max-agents": func(v string) error {
			n, ok := pyInt(v)
			if !ok || n.Cmp(big.NewInt(1)) < 0 {
				return errBadValue
			}
			a.MaxAgents = n.String()
			return nil
		},
		"--port": func(v string) error {
			n, ok := pyInt(v)
			if !ok || n.Cmp(big.NewInt(1)) < 0 || n.Cmp(big.NewInt(65535)) > 0 {
				return errBadValue
			}
			a.Port = int(n.Int64())
			return nil
		},
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if hasHelpArg[arg] {
			return nil, helpRequested{}
		}
		name, val, hasVal := strings.Cut(arg, "=") // --factory=Label=URL: the value keeps its own "="
		if !strings.HasPrefix(arg, "--") {
			return nil, usageErr("unrecognized option (values are not echoed)")
		}
		if b, ok := boolFlags[name]; ok {
			if hasVal {
				return nil, usageErr("argument " + name + " rejected (values are not echoed)")
			}
			*b = true
			continue
		}
		set, ok := valueFlags[name]
		if !ok {
			return nil, usageErr("unrecognized option (values are not echoed)")
		}
		if !hasVal {
			if i+1 >= len(argv) {
				return nil, usageErr("argument " + name + " rejected (values are not echoed)")
			}
			i++
			val = argv[i]
			if strings.HasPrefix(val, "-") && val != "-" && !negNumRe.MatchString(val) {
				return nil, usageErr("argument " + name + " rejected (values are not echoed)")
			}
		}
		if err := set(val); err != nil {
			return nil, usageErr("argument " + name + " rejected (values are not echoed)")
		}
	}
	return a, a.validate()
}

var errBadValue = stopf(2, "bad value")

func (a *Args) validate() error {
	modes := 0
	for _, b := range []bool{a.Check, a.DryRun, a.Yes} {
		if b {
			modes++
		}
	}
	if modes != 1 {
		return usageErr("pass exactly one of --check, --dry-run, --yes")
	}
	if a.UninstallBootUnits && a.Check {
		return usageErr("--uninstall-boot-units goes with --dry-run or --yes")
	}
	dashSet := a.Factories != nil || a.NoSwitch || a.NoSidebar
	if !a.HerdrApp && dashSet {
		return usageErr("--factory, --no-switch and --no-sidebar need --herdr-app")
	}
	if a.HerdrApp {
		if a.Check {
			return usageErr("--herdr-app goes with --dry-run or --yes")
		}
		for _, spec := range a.Factories {
			if _, _, ok := parseFactory(spec); !ok {
				return usageErr("--factory takes URL or LABEL=URL, with an http(s) URL")
			}
		}
	}
	if (a.ProjectID != "") != (a.ProjectRepo != "") {
		return usageErr("--project-id and --project-repo go together")
	}
	if (a.ProjectName != "" || a.ProjectGH != "") && a.ProjectID == "" {
		return usageErr("--project-name and --project-gh need --project-id and --project-repo")
	}
	if strings.HasPrefix(a.Ref, "-") || strings.HasPrefix(a.RepoURL, "-") {
		return usageErr("--ref and --repo-url must not start with '-'")
	}
	return nil
}
