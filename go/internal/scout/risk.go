package scout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Port of scout/risk.py: install-risk classification of a git tree listing. Pure: nothing is fetched, executed
// or written here.

// FlagOrder is the order of risk flags in Result.Risk.
var FlagOrder = []string{"scripts", "hooks", "mcp", "symlinks", "shell-in-markdown", "network", "unknown"}

const (
	MaxMDReads      = 10
	MaxJSONReads    = 3
	MaxDetail       = 20
	ReadCap         = 65536
	MaxReadmeReads  = 3
	MaxShebangReads = 10
	MaxConfigReads  = 10
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

var (
	allowedExt       = set(".md", ".txt", ".csv")
	allowedJSONNames = set("plugin.json", "marketplace.json")
	allowedBare      = []string{"LICENSE", "NOTICE", "README"}

	scriptExt = set(".sh", ".bash", ".zsh", ".fish", ".ksh", ".csh", ".py", ".pyw", ".js", ".cjs", ".mjs", ".jsx", ".ts", ".tsx",
		".rb", ".pl", ".pm", ".php", ".lua", ".r", ".ps1", ".psm1", ".bat", ".cmd", ".vbs", ".exe", ".dll", ".com",
		".msi", ".bin", ".so", ".dylib", ".a", ".o", ".jar", ".class", ".wasm", ".app", ".deb", ".rpm", ".apk",
		".whl", ".pyc", ".node", ".scpt", ".command", ".run", ".out", ".elf", ".awk", ".tcl", ".groovy", ".kts")
	inertExt = set(".md", ".markdown", ".txt", ".rst", ".csv", ".tsv", ".json", ".jsonc", ".json5", ".yaml", ".yml", ".toml",
		".ini", ".cfg", ".xml", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp", ".svg", ".avif",
		".woff", ".woff2", ".ttf", ".otf", ".eot", ".pdf", ".lock", ".html", ".htm", ".css", ".patch")
	inertNames = set(".gitignore", ".gitattributes", ".gitmodules", ".editorconfig", ".npmignore", ".dockerignore", ".prettierrc",
		".gitkeep", ".keep", "license", "licence", "notice", "readme", "copying", "authors", "changelog", "codeowners")
	scriptNames = set("package.json", "makefile", "dockerfile", "justfile", "rakefile", "gemfile", "tasks.json", "devcontainer.json",
		".devcontainer.json", "composer.json", "pyproject.toml", "setup.cfg", "setup.py", "lefthook.yml", "lefthook.yaml",
		".pre-commit-config.yaml", ".lsp.json", "settings.json", "settings.local.json")
	configExt  = set(".json", ".jsonc", ".json5", ".yaml", ".yml", ".toml")
	jsonCfgExt = set(".json", ".jsonc", ".json5")

	gitSafeSub  = set("status", "log", "diff", "show", "rev-parse", "ls-files", "blame")
	keywordCmd  = set(strings.Fields("time noglob nocorrect builtin command exec coproc if then while until for case select function eval source nohup env sudo doas - ! { [[ .")...)
	skillsJSONR = regexp.MustCompile(`(?:^|/)skills/[^/]+/.+`)
)

var (
	cfgCommand     = regexp.MustCompile("(?m)[\"']command[\"']\\s*:|^\\s*command\\s*=|[\"']lspServers[\"']\\s*:")
	cfgCommandTOML = regexp.MustCompile("(?m)(?:^|[{,])[ \\t]*(?:[" + wordClass + "-]+\\.)*[\"']?command[\"']?[ \\t]*=")
	cfgCommandYAML = regexp.MustCompile("(?m)(?:^[ \\t]*(?:-[ \\t]+)?|[{,][ \\t]*)[\"']?command[\"']?[ \\t]*:(?:[ \\t\\r\\n\\f\\v]|$)")
	uEscape        = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
	cfgHooks       = regexp.MustCompile("(?m)[\"']hooks[\"']\\s*[:=]|^\\s*hooks\\s*[:=]|^\\s*\\[+\\s*hooks\\b")

	// ShellBang is exported: vendor's tree check uses it on written markdown.
	ShellBang = regexp.MustCompile("(?m)(?:^|[" + wsClass + `(])!` + "`[^`\\n]+`|^[" + wsClass + "]*```!")
	netWords  = regexp.MustCompile(`curl|wget|WebFetch`)
	netVerbs  = regexp.MustCompile(`(?i)fetch|download|retrieve`)
	netTail   = regexp.MustCompile(`(?i)^[^\n]{0,40}https?://`)
	fmNetWord = regexp.MustCompile(`WebFetch|WebSearch`)
	mcpAdd    = regexp.MustCompile("(?i)\\bmcp[" + wsClass + "]+add(?:-json|-from-claude-desktop)?\\b")
	mcpKey    = regexp.MustCompile(`mcpServers|mcp_servers`)
	urlRe     = regexp.MustCompile("(?i)\\bhttps?://([^" + wsClass + "/?#)>\\]\"'`]+)([^" + wsClass + ">\\]\"'`]*)")
	mcpLaunch = regexp.MustCompile("(?i)\\b(?:npx|bunx|uvx|pipx[" + wsClass + "]{1,8}run|pnpm[" + wsClass + "]{1,8}dlx|npm[" + wsClass +
		"]{1,8}exec|yarn[" + wsClass + "]{1,8}dlx)\\b[^\\n]{0,200}?(?:mcp|modelcontextprotocol)")
	fmBashNet    = regexp.MustCompile(`Bash\([^)]*\b(?:curl|wget|nc|netcat|ssh|scp|ftp)\b`)
	fmHooks      = regexp.MustCompile(`(?m)^hooks[` + wsClass + `]*:`)
	fmToolKey    = regexp.MustCompile("(?m)^(?:allowed-tools|tools)[" + wsClass + "]*:[^\\n]*(?:\\n(?:[ \\t]+[^\\n]*|-[^\\n]*))*")
	bashScoped   = regexp.MustCompile("Bash[" + wsClass + "]*\\(([^)]*)\\)")
	bashFollowOK = regexp.MustCompile("^[" + wsClass + "]*\\(")
	openCmd      = regexp.MustCompile(`(?i)^(?:(?:ba|z|da|k|c|tc|fi)?sh|fish|pwsh|(?:python|ruby|perl|node|php)[\d.]*|env|eval|exec|source|\.` +
		`|xargs|sudo|doas|timeout|nohup|command|nice|stdbuf|setsid` +
		`|npx|uv|uvx|deno|bun|awk|gawk|find|git|make` +
		`|ssh|scp|rsync|busybox|chroot|unshare|nsenter|script|expect|tclsh|lua|R|Rscript|julia)$`)
	envAssign  = regexp.MustCompile(`^[A-Za-z_][` + wordClass + `]*=`)
	shellMeta  = regexp.MustCompile("[;&|<>$`()\\\\*?\\n]")
	wordMeta   = regexp.MustCompile("[$`\\[\\]{}~*?()]")
	wordOK     = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)
	quoteStrip = strings.NewReplacer(`\`, "", `'`, "", `"`, "")
	contLine   = regexp.MustCompile("\\\\\r?\n[ \\t]*")
	fmBlock    = regexp.MustCompile(`(?s)\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\z)`)
)

// Entry is one `git/trees/<sha>?recursive=1` row.
type Entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Sha  string `json:"sha"`
	Size int64  `json:"size"`
}

// Result is the outcome of Classify. Risk is a comma list in FlagOrder, or "prompt-only".
type Result struct {
	Risk   string
	Detail []string
	Kind   string
}

// ReadError means a blob could not be read; the check that needed it reports "unknown".
type ReadError struct{ Msg string }

func (e *ReadError) Error() string { return e.Msg }

// ReadFunc supplies blob text. Any error is treated as unreadable (the check fails closed to "unknown").
type ReadFunc func(Entry) (string, error)

type scoped struct {
	rel string
	e   Entry
}

// Scoped is _scoped: entries under prefix as (relative path, entry); prefix "" means the whole tree.
func scopedEntries(tree []Entry, prefix string) []scoped {
	prefix = strings.Trim(prefix, "/")
	var out []scoped
	for _, e := range tree {
		switch {
		case prefix == "":
			out = append(out, scoped{e.Path, e})
		case strings.HasPrefix(e.Path, prefix+"/"):
			out = append(out, scoped{e.Path[len(prefix)+1:], e})
		}
	}
	return out
}

// HasManifest is has_manifest: the scoped tree holds a plugin manifest, a SKILL.md, an agent or a command file.
func HasManifest(tree []Entry, prefix string) bool {
	for _, it := range scopedEntries(tree, prefix) {
		if it.e.Type == "tree" {
			continue
		}
		segs := strings.Split(it.rel, "/")
		base := segs[len(segs)-1]
		parent := ""
		if len(segs) > 1 {
			parent = segs[len(segs)-2]
		}
		lp := strings.ToLower(it.rel)
		if strings.ToLower(base) == "skill.md" || strings.HasSuffix(lp, ".claude-plugin/plugin.json") || lp == "plugin.json" {
			return true
		}
		lparent := strings.ToLower(parent)
		if (lparent == "agents" || lparent == "commands") && strings.HasSuffix(strings.ToLower(base), ".md") {
			return true
		}
	}
	return false
}

// IsAllowed is _is_allowed: the strict allowlist of what may be copied into the eval sandbox.
func IsAllowed(full string) bool {
	base := full[strings.LastIndex(full, "/")+1:]
	low := strings.ToLower(base)
	ext := ""
	if dot := strings.LastIndex(low, "."); dot > 0 {
		ext = low[dot:]
	}
	if allowedExt[ext] || allowedJSONNames[low] {
		return true
	}
	if ext == ".json" {
		return skillsJSONR.MatchString(full)
	}
	if ext == "" {
		up := strings.ToUpper(base)
		for _, p := range allowedBare {
			if strings.HasPrefix(up, p) {
				return true
			}
		}
	}
	return false
}

func fileClass(base string) string {
	low := strings.ToLower(base)
	ext := ""
	if dot := strings.LastIndex(low, "."); dot > 0 {
		ext = low[dot:]
	}
	if scriptNames[low] || scriptExt[ext] {
		return "script"
	}
	stem := low
	if ext == "" {
		stem = strings.SplitN(low, ".", 2)[0]
	}
	if inertNames[low] || (ext == "" && inertNames[stem]) || inertExt[ext] {
		return "inert"
	}
	return "other"
}

// bashUnrestrictedBare is BASH_UNRESTRICTED.search: a bare `Bash` (not followed by `(`, a word char or `-`).
func bashUnrestrictedBare(tools string) bool {
	for off := 0; ; {
		i := strings.Index(tools[off:], "Bash")
		if i < 0 {
			return false
		}
		i += off
		off = i + 1
		if !prevNotWordDash(tools, i) {
			continue
		}
		rest := tools[i+4:]
		if rest != "" {
			r, _ := utf8.DecodeRuneInString(rest)
			if r == '-' || isWordRune(r) {
				continue
			}
		}
		if bashFollowOK.MatchString(rest) {
			continue
		}
		return true
	}
}

// bashScopes is BASH_SCOPED.finditer with its (?<![\w-]) lookbehind: the captured scopes in order.
func bashScopes(tools string) []string {
	var out []string
	for pos := 0; pos <= len(tools); {
		loc := bashScoped.FindStringSubmatchIndex(tools[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[0]
		if prevNotWordDash(tools, start) {
			out = append(out, tools[pos+loc[2]:pos+loc[3]])
			pos += loc[1]
		} else {
			pos = start + 1
		}
	}
	return out
}

func anyMeta(words []string, re *regexp.Regexp) bool {
	for _, w := range words {
		if strings.HasPrefix(w, "-") || re.MatchString(w) {
			return true
		}
	}
	return false
}

// unrestrictedBash is _unrestricted_bash: bare `Bash`, `Bash(*)`, `Bash()` and scopes on a shell or interpreter.
func unrestrictedBash(tools string) bool {
	if bashUnrestrictedBare(tools) {
		return true
	}
	for _, scope := range bashScopes(tools) {
		raw := pyTrim(scope)
		arg := quoteStrip.Replace(raw)
		head, tail, hasColon := strings.Cut(arg, ":")
		words := pyFields(head)
		if len(words) == 0 || envAssign.MatchString(words[0]) {
			return true
		}
		rawHead, _, _ := strings.Cut(raw, ":")
		rawWords := pyFields(rawHead)
		if len(rawWords) == 0 || wordMeta.MatchString(rawWords[0]) || wordMeta.MatchString(words[0]) {
			return true
		}
		if strings.Contains(raw, "\n") || !wordOK.MatchString(words[0]) {
			return true
		}
		cmd := words[0][strings.LastIndex(words[0], "/")+1:]
		if cmd == "" || keywordCmd[cmd] {
			return true
		}
		var after []string
		if hasColon {
			after = pyFields(tail)
		}
		if cmd == "git" && words[0] == "git" && len(words) > 1 && gitSafeSub[words[1]] && !anyMeta(words[2:], shellMeta) {
			ok := true
			for _, w := range after {
				if w != "*" && (strings.HasPrefix(w, "-") || shellMeta.MatchString(w)) {
					ok = false
				}
			}
			if ok {
				continue
			}
		}
		if openCmd.MatchString(cmd) {
			return true
		}
	}
	return false
}

// mcpText is _mcp_text: (declares/instructs MCP, names a remote MCP endpoint URL) for fetched text.
func mcpText(text string) (bool, bool) {
	text = contLine.ReplaceAllString(text, " ")
	text = strings.NewReplacer(`\/`, "/", `\u002f`, "/", `\u002F`, "/").Replace(text)
	launcher := mcpLaunch.MatchString(text)
	remote := launcher
	if !remote {
		for _, m := range urlRe.FindAllStringSubmatch(text, -1) {
			if strings.Contains(strings.ToLower(m[1]+m[2]), "mcp") {
				remote = true
				break
			}
		}
	}
	return mcpAdd.MatchString(text) || mcpKey.MatchString(text) || remote, remote
}

// mcpTool is MCP_TOOL.search: (?<![\w-])mcp__[\w-].
func mcpTool(s string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], "mcp__")
		if i < 0 {
			return false
		}
		i += off
		off = i + 1
		if !prevNotWordDash(s, i) {
			continue
		}
		rest := s[i+5:]
		if rest == "" {
			continue
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if r == '-' || isWordRune(r) {
			return true
		}
	}
}

// normalize drops a BOM, zero-width space and leading (Unicode) whitespace so the `---` fence is at the start.
func normalize(text string) string {
	return strings.TrimLeftFunc(text, func(r rune) bool { return isPySpace(r) || r == '\ufeff' || r == '\u200b' })
}

// frontmatter returns (block, ok): ok is false when the text opens a fence it never closes.
func frontmatter(text string) (string, bool) {
	if m := fmBlock.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	if strings.HasPrefix(text, "---") {
		return "", false
	}
	return "", true
}

// DetectKind is detect_kind.
func DetectKind(rels []string) string {
	low := make([]string, len(rels))
	for i, r := range rels {
		low[i] = strings.ToLower(r)
	}
	var skills int
	anyOf := func(f func(string) bool) bool {
		for _, r := range low {
			if f(r) {
				return true
			}
		}
		return false
	}
	for _, r := range low {
		if r[strings.LastIndex(r, "/")+1:] == "skill.md" {
			skills++
		}
	}
	if anyOf(func(r string) bool {
		return strings.HasSuffix(r, "plugin.json") && strings.Contains(r, ".claude-plugin")
	}) {
		return "plugin"
	}
	if skills > 1 {
		return "collection"
	}
	if skills == 1 {
		return "skill"
	}
	if anyOf(func(r string) bool {
		return strings.HasSuffix(r, ".md") && (strings.HasPrefix(r, "agents/") || strings.Contains(r, "/agents/"))
	}) {
		return "agent"
	}
	if anyOf(func(r string) bool {
		segs := strings.Split(r, "/")
		for _, s := range segs[:len(segs)-1] {
			if s == "hooks" {
				return true
			}
		}
		return strings.HasSuffix(r, "hooks.json")
	}) {
		return "hook"
	}
	if anyOf(func(r string) bool { return strings.HasSuffix(r, ".mcp.json") }) {
		return "mcp"
	}
	if anyOf(func(r string) bool {
		return strings.HasSuffix(r, ".md") && (strings.HasPrefix(r, "commands/") || strings.Contains(r, "/commands/"))
	}) {
		return "plugin"
	}
	return "unknown"
}

// jsonKeys is _json_keys: every dict key in parsed JSON, iteratively.
func jsonKeys(data any) map[string]bool {
	keys := map[string]bool{}
	stack := []any{data}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch v := n.(type) {
		case map[string]any:
			for k, c := range v {
				keys[k] = true
				stack = append(stack, c)
			}
		case []any:
			stack = append(stack, v...)
		}
	}
	return keys
}

func parseJSON(text string) (any, bool) {
	var data any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return nil, false
	}
	return data, true
}

// configFlags is _config_flags: (hooks, scripts, mcpKey) for a json/yaml/toml config.
func configFlags(rel, text string) (bool, bool, bool) {
	text = strings.TrimLeft(text, "\ufeff")
	if jsonCfgExt[strings.ToLower(rfindExt(rel))] {
		if data, ok := parseJSON(text); ok {
			k := jsonKeys(data)
			return k["hooks"], k["command"] || k["lspServers"], k["mcpServers"] || k["mcp_servers"]
		}
	}
	text = uEscape.ReplaceAllStringFunc(text, func(s string) string {
		var n int
		fmt.Sscanf(s[2:], "%x", &n)
		return string(rune(n))
	})
	hooks := cfgHooks.MatchString(text)
	scripts := cfgCommand.MatchString(text) || cfgCommandTOML.MatchString(text) || cfgCommandYAML.MatchString(text)
	return hooks, scripts, false
}

type flagSet map[string][]string

func (f flagSet) add(flag, v string) { f[flag] = append(f[flag], v) }

func readInto(read ReadFunc, e Entry, flags flagSet, rel string) (string, bool) {
	if read == nil {
		flags.add("unknown", "not inspected: "+rel)
		return "", false
	}
	text, err := read(e)
	if err != nil {
		msg := err.Error()
		if runeLen(msg) > 80 {
			msg = runePrefix(msg, 80)
		}
		flags.add("unknown", fmt.Sprintf("unreadable: %s (%s)", rel, msg))
		return "", false
	}
	if runeLen(text) > ReadCap || e.Size > ReadCap {
		flags.add("unknown", fmt.Sprintf("over %d bytes, not fully scanned: %s", ReadCap, rel))
	}
	return runePrefix(text, ReadCap), true
}

// Classify is classify: the install risk of the files under prefix in tree. read supplies blob text; without
// it markdown is not inspected and the result says so.
func Classify(tree []Entry, prefix string, read ReadFunc, truncated bool) Result {
	items := scopedEntries(tree, prefix)
	flags := flagSet{}
	var rels []string
	for _, it := range items {
		if it.e.Type != "tree" {
			rels = append(rels, it.rel)
		}
	}
	var mdReads, jsonReads, readmeReads, sniff, cfgReads []scoped
	for _, it := range items {
		rel, e := it.rel, it.e
		if e.Type == "tree" {
			continue
		}
		segs := strings.Split(rel, "/")
		base := segs[len(segs)-1]
		if e.Mode == "120000" || e.Mode == "160000" || e.Type == "commit" {
			flags.add("symlinks", rel)
			continue
		}
		lsegs := map[string]bool{}
		hookDir := false
		for _, s := range segs[:len(segs)-1] {
			l := strings.ToLower(s)
			lsegs[l] = true
			if l == "hooks" {
				hookDir = true
			}
		}
		low := strings.ToLower(base)
		if hookDir || low == "hooks.json" {
			flags.add("hooks", rel)
		}
		if low == "hooks.json" {
			continue
		}
		if low == ".mcp.json" {
			flags.add("mcp", rel)
			continue
		}
		if e.Mode == "100755" || fileClass(base) == "script" {
			flags.add("scripts", rel)
			continue
		}
		if strings.Contains(low, ".mcp.") && strings.HasSuffix(low, ".json") {
			flags.add("mcp", rel)
			continue
		}
		if fileClass(base) == "other" {
			sniff = append(sniff, it)
			continue
		}
		cmdAgent := lsegs["commands"] || lsegs["agents"]
		isReadme := (low == "readme.md" || low == "readme.markdown") && !cmdAgent
		if isReadme {
			readmeReads = append(readmeReads, it)
		}
		switch {
		case low == "plugin.json":
			jsonReads = append(jsonReads, it)
		case configExt[rfindExt(low)] && low != "marketplace.json":
			cfgReads = append(cfgReads, it)
		case isReadme:
		case low == "skill.md" || (strings.HasSuffix(low, ".md") && cmdAgent):
			mdReads = append(mdReads, it)
		}
	}

	unread := 0
	for i, it := range sniff {
		text, got := "", false
		if read != nil && i < MaxShebangReads {
			if t, err := read(it.e); err == nil {
				text, got = t, true
			}
		}
		if got && strings.HasPrefix(text, "#!") {
			flags.add("scripts", it.rel)
		} else {
			flags.add("unknown", "unrecognised file type: "+it.rel)
		}
	}
	for _, it := range jsonReads[:min(len(jsonReads), MaxJSONReads)] {
		text, ok := readInto(read, it.e, flags, it.rel)
		if !ok {
			continue
		}
		keys := map[string]bool{}
		data, parsed := parseJSON(strings.TrimLeft(text, "\ufeff"))
		if obj, isObj := data.(map[string]any); parsed && isObj {
			for k := range obj {
				keys[k] = true
			}
		} else {
			for _, k := range []string{"hooks", "mcpServers"} {
				if strings.Contains(text, `"`+k+`"`) {
					keys[k] = true
				}
			}
			flags.add("unknown", "unparseable json: "+it.rel)
		}
		if keys["hooks"] {
			flags.add("hooks", it.rel)
		}
		if keys["lspServers"] || strings.Contains(text, `"lspServers"`) {
			flags.add("scripts", it.rel)
		}
		isMCP, remote := mcpText(text)
		if keys["mcpServers"] || isMCP {
			flags.add("mcp", it.rel)
		}
		if remote || (keys["mcpServers"] && urlRe.MatchString(strings.ReplaceAll(text, `\/`, "/"))) {
			flags.add("network", it.rel)
		}
	}
	unread += max(0, len(jsonReads)-MaxJSONReads)
	for _, it := range cfgReads[:min(len(cfgReads), MaxConfigReads)] {
		text, ok := readInto(read, it.e, flags, it.rel)
		if !ok {
			continue
		}
		hasHooks, hasCommand, mcpKeyFound := configFlags(it.rel, text)
		if hasHooks {
			flags.add("hooks", it.rel)
		}
		if hasCommand {
			flags.add("scripts", it.rel)
		}
		isMCP, remote := mcpText(text)
		if isMCP || mcpKeyFound || (strings.Contains(strings.ToLower(it.rel[strings.LastIndex(it.rel, "/")+1:]), "mcp") && hasCommand) {
			flags.add("mcp", it.rel)
		}
		if remote {
			flags.add("network", it.rel)
		}
	}
	unread += max(0, len(cfgReads)-MaxConfigReads)
	for _, it := range readmeReads[:min(len(readmeReads), MaxReadmeReads)] {
		text, ok := readInto(read, it.e, flags, it.rel)
		if !ok {
			continue
		}
		isMCP, remote := mcpText(text)
		if isMCP {
			flags.add("mcp", it.rel)
		}
		if remote {
			flags.add("network", it.rel)
		}
	}
	unread += max(0, len(readmeReads)-MaxReadmeReads)
	sort.SliceStable(mdReads, func(i, j int) bool {
		ki := strings.ToLower(mdReads[i].rel[strings.LastIndex(mdReads[i].rel, "/")+1:]) != "skill.md"
		kj := strings.ToLower(mdReads[j].rel[strings.LastIndex(mdReads[j].rel, "/")+1:]) != "skill.md"
		if ki != kj {
			return !ki
		}
		return mdReads[i].rel < mdReads[j].rel
	})
	for _, it := range mdReads[:min(len(mdReads), MaxMDReads)] {
		text, ok := readInto(read, it.e, flags, it.rel)
		if !ok {
			continue
		}
		text = normalize(text)
		fm, closed := frontmatter(text)
		var body string
		if !closed {
			flags.add("unknown", "unclosed frontmatter: "+it.rel)
			fm, body = text, text
		} else if fm != "" {
			body = text[len(fm):]
		} else {
			body = text
		}
		isMCP, remote := mcpText(text)
		if isMCP {
			flags.add("mcp", it.rel)
		}
		if remote {
			flags.add("network", it.rel)
		}
		if ShellBang.MatchString(text) || fmBashNet.MatchString(fm) {
			flags.add("shell-in-markdown", it.rel)
		}
		if fmHooks.MatchString(fm) {
			flags.add("hooks", it.rel)
		}
		tools := strings.Join(fmToolKey.FindAllString(fm, -1), "\n")
		if mcpTool(tools) {
			flags.add("mcp", it.rel)
		}
		openBash := unrestrictedBash(tools)
		if wordMatch(fmNetWord, fm, nil) || netBodyMatch(body) || openBash {
			flags.add("network", it.rel)
		}
		if openBash && !bashUnrestrictedBare(tools) {
			flags.add("shell-in-markdown", it.rel)
		}
	}
	unread += max(0, len(mdReads)-MaxMDReads)
	if unread > 0 {
		flags.add("unknown", fmt.Sprintf("unread: %d file(s) over the read limit", unread))
	}
	if truncated {
		flags.add("unknown", "tree truncated by the API")
	}
	if len(rels) == 0 {
		flags.add("unknown", "no files under path")
	}

	var present, detail []string
	for _, f := range FlagOrder {
		if len(flags[f]) == 0 {
			continue
		}
		present = append(present, f)
		seen := map[string]bool{}
		var uniq []string
		for _, p := range flags[f] {
			if !seen[p] {
				seen[p] = true
				uniq = append(uniq, p)
			}
		}
		sort.Strings(uniq)
		for _, p := range uniq {
			detail = append(detail, f+": "+p)
		}
	}
	if len(detail) > MaxDetail {
		detail = detail[:MaxDetail]
	}
	risk := strings.Join(present, ",")
	if risk == "" {
		risk = "prompt-only"
	}
	return Result{Risk: risk, Detail: detail, Kind: DetectKind(rels)}
}

// wordMatch is `\b(?:words)\b` (optionally followed by a tail) with Python's Unicode word boundary: re finds each
// candidate and the boundaries are checked by hand, since RE2's \b is ASCII only.
func wordMatch(re *regexp.Regexp, s string, tail func(rest string) bool) bool {
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if !prevNotWord(s, loc[0]) || !nextNotWord(s, loc[1]) {
			continue
		}
		if tail == nil || tail(s[loc[1]:]) {
			return true
		}
	}
	return false
}

func prevNotWord(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWordRune(r)
}

func nextNotWord(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return !isWordRune(r)
}

// netBodyMatch is NET_BODY.search.
func netBodyMatch(body string) bool {
	return wordMatch(netWords, body, nil) ||
		wordMatch(netVerbs, body, func(rest string) bool { return netTail.MatchString(rest) })
}
