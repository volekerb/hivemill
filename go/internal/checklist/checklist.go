// Package checklist matches reliability-checklist items (ci/diff-checklist.json)
// to a diff, renders the PR-body block and verifies the filled-in block.
// Stdlib only. See docs/diff-checklist.md for the schema and the contract.
package checklist

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Mode values of the items file.
const (
	ModeObserve = "observe"
	ModeEnforce = "enforce"
)

var (
	idRe     = regexp.MustCompile(`^CL-[0-9]{2,3}$`)
	sourceRe = regexp.MustCompile(`^PR#[0-9]+$`)
)

// Item is one validated checklist item.
type Item struct {
	ID           string
	Title        string
	Paths        []string
	Added        []string
	ExcludePaths []string
	Check        string
	Sources      []string

	pathRes    []*regexp.Regexp
	addedRes   []*regexp.Regexp
	excludeRes []*regexp.Regexp
}

// List is a validated items file.
type List struct {
	Version int
	Mode    string
	Items   []Item
}

// ValidationError carries every problem found, one per line, each naming the item and field.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string { return strings.Join(e.Problems, "\n") }

// Change is one changed path of a diff with the lines it adds.
type Change struct {
	Path  string
	Added []string
}

func decodeObject(raw json.RawMessage, allowed []string, where string, probs *[]string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		*probs = append(*probs, where+": must be a JSON object")
		return nil
	}
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !ok[k] {
			*probs = append(*probs, fmt.Sprintf("%s: unknown key %q (allowed: %s)", where, k, strings.Join(allowed, ", ")))
		}
	}
	return m
}

func strList(raw json.RawMessage, where string, probs *[]string) []string {
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil || l == nil {
		*probs = append(*probs, where+": must be an array of strings")
		return nil
	}
	if len(l) == 0 {
		*probs = append(*probs, where+": must not be empty when present")
		return nil
	}
	for i, s := range l {
		if strings.TrimSpace(s) == "" {
			*probs = append(*probs, fmt.Sprintf("%s[%d]: must not be empty", where, i))
			return nil
		}
	}
	return l
}

func oneLine(raw json.RawMessage, where string, probs *[]string) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*probs = append(*probs, where+": must be a string")
		return ""
	}
	if strings.TrimSpace(s) == "" {
		*probs = append(*probs, where+": must be non-empty")
		return ""
	}
	if strings.ContainsAny(s, "\r\n") {
		*probs = append(*probs, where+": must be a single line")
		return ""
	}
	return s
}

// Load parses and strictly validates an items file.
func Load(data []byte) (*List, error) {
	var probs []string
	top := decodeObject(data, []string{"version", "mode", "items"}, "file", &probs)
	if top == nil {
		return nil, &ValidationError{probs}
	}
	l := &List{}
	if raw, ok := top["version"]; !ok {
		probs = append(probs, "file: field version: required")
	} else if err := json.Unmarshal(raw, &l.Version); err != nil || l.Version != 1 {
		probs = append(probs, "file: field version: must be the number 1")
	}
	if raw, ok := top["mode"]; !ok {
		probs = append(probs, "file: field mode: required")
	} else if err := json.Unmarshal(raw, &l.Mode); err != nil || (l.Mode != ModeObserve && l.Mode != ModeEnforce) {
		probs = append(probs, `file: field mode: must be "observe" or "enforce"`)
	}
	var rawItems []json.RawMessage
	if raw, ok := top["items"]; !ok {
		probs = append(probs, "file: field items: required")
	} else if err := json.Unmarshal(raw, &rawItems); err != nil || rawItems == nil {
		probs = append(probs, "file: field items: must be an array")
	}
	seen := map[string]bool{}
	for i, raw := range rawItems {
		it, p := loadItem(raw, i, seen)
		probs = append(probs, p...)
		l.Items = append(l.Items, it)
	}
	if len(probs) > 0 {
		return nil, &ValidationError{probs}
	}
	return l, nil
}

func loadItem(raw json.RawMessage, idx int, seen map[string]bool) (Item, []string) {
	var probs []string
	var it Item
	name := fmt.Sprintf("items[%d]", idx)
	m := decodeObject(raw, []string{"id", "title", "trigger", "check", "sources"}, name, &probs)
	if m == nil {
		return it, probs
	}
	if r, ok := m["id"]; ok {
		var id string
		if json.Unmarshal(r, &id) == nil && idRe.MatchString(id) {
			it.ID = id
			name = "item " + id
			if seen[id] {
				probs = append(probs, name+": field id: duplicate id")
			}
			seen[id] = true
		} else {
			probs = append(probs, name+": field id: must be CL- followed by 2 or 3 digits")
		}
	} else {
		probs = append(probs, name+": field id: required")
	}
	req := func(key string) (json.RawMessage, bool) {
		r, ok := m[key]
		if !ok {
			probs = append(probs, name+": field "+key+": required")
		}
		return r, ok
	}
	if r, ok := req("title"); ok {
		it.Title = oneLine(r, name+": field title", &probs)
	}
	if r, ok := req("check"); ok {
		it.Check = oneLine(r, name+": field check", &probs)
	}
	if r, ok := req("sources"); ok {
		it.Sources = loadSources(r, name+": field sources", &probs)
	}
	if r, ok := req("trigger"); ok {
		loadTrigger(&it, r, name, &probs)
	}
	return it, probs
}

func loadSources(raw json.RawMessage, where string, probs *[]string) []string {
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		*probs = append(*probs, where+": must be an array of PR#<digits> strings")
		return nil
	}
	distinct := map[string]bool{}
	for i, s := range l {
		if !sourceRe.MatchString(s) {
			*probs = append(*probs, fmt.Sprintf("%s[%d]: %q is not of the form PR#<digits>", where, i, s))
			continue
		}
		distinct[s] = true
	}
	if len(distinct) < 2 {
		*probs = append(*probs, where+": needs at least 2 distinct PR#<digits> entries")
	}
	return l
}

func loadTrigger(it *Item, raw json.RawMessage, name string, probs *[]string) {
	where := name + ": field trigger"
	m := decodeObject(raw, []string{"paths", "added", "exclude_paths"}, where, probs)
	if m == nil {
		return
	}
	_, hasPaths := m["paths"]
	_, hasAdded := m["added"]
	if !hasPaths && !hasAdded {
		*probs = append(*probs, where+": empty trigger, needs paths and/or added")
	}
	if r, ok := m["paths"]; ok {
		it.Paths = strList(r, where+".paths", probs)
		it.pathRes = compileGlobs(it.Paths, where+".paths", probs)
	}
	if r, ok := m["exclude_paths"]; ok {
		it.ExcludePaths = strList(r, where+".exclude_paths", probs)
		it.excludeRes = compileGlobs(it.ExcludePaths, where+".exclude_paths", probs)
	}
	if r, ok := m["added"]; ok {
		it.Added = strList(r, where+".added", probs)
		for i, s := range it.Added {
			re, err := regexp.Compile(s)
			if err != nil {
				*probs = append(*probs, fmt.Sprintf("%s.added[%d]: bad regex: %v", where, i, err))
				continue
			}
			it.addedRes = append(it.addedRes, re)
		}
	}
}

func compileGlobs(globs []string, where string, probs *[]string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for i, g := range globs {
		re, err := CompileGlob(g)
		if err != nil {
			*probs = append(*probs, fmt.Sprintf("%s[%d]: bad glob: %v", where, i, err))
			continue
		}
		out = append(out, re)
	}
	return out
}

// CompileGlob turns a glob into an anchored regexp. `**` crosses directories,
// `*` and `?` do not cross `/`; `**/` also matches zero directories.
// Bracket, brace and backslash syntax is not supported and is a validation error.
func CompileGlob(g string) (*regexp.Regexp, error) {
	if g == "" {
		return nil, fmt.Errorf("empty glob")
	}
	var sb strings.Builder
	sb.WriteString(`^(?s:`)
	rs := []rune(g)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				i++
				if i+1 < len(rs) && rs[i+1] == '/' {
					i++
					sb.WriteString(`(?:.*/)?`)
				} else {
					sb.WriteString(`.*`)
				}
			} else {
				sb.WriteString(`[^/]*`)
			}
		case '?':
			sb.WriteString(`[^/]`)
		case '[', ']', '{', '}', '\\', 0:
			return nil, fmt.Errorf("unsupported character %q in glob %q", r, g)
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString(`)$`)
	return regexp.Compile(sb.String())
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func (it *Item) matches(changes []Change) bool {
	for _, c := range changes {
		if len(it.pathRes) > 0 && !anyMatch(it.pathRes, c.Path) {
			continue
		}
		if anyMatch(it.excludeRes, c.Path) {
			continue
		}
		if len(it.addedRes) == 0 {
			return true
		}
		for _, line := range c.Added {
			if anyMatch(it.addedRes, line) {
				return true
			}
		}
	}
	return false
}

// Match returns the items triggered by the changes, in file order.
func Match(items []Item, changes []Change) []Item {
	var out []Item
	for i := range items {
		if items[i].matches(changes) {
			out = append(out, items[i])
		}
	}
	return out
}

// RenderBlock renders the PR-body block. ref and sha identify the items source.
func RenderBlock(ref, sha, mode string, matched []Item, total int) string {
	var sb strings.Builder
	sb.WriteString("<details><summary>Diff checklist</summary>\n\n")
	sb.WriteString("## Diff checklist\n")
	fmt.Fprintf(&sb, "items: %s@%s · mode: %s · matched %d of %d\n", ref, sha, mode, len(matched), total)
	if len(matched) == 0 {
		sb.WriteString("- none matched\n")
	}
	for _, it := range matched {
		fmt.Fprintf(&sb, "- %s %s | check: %s | result: TODO\n", it.ID, it.Title, it.Check)
	}
	sb.WriteString("</details>\n")
	return sb.String()
}

// Result is the verdict for one matched item.
type Result struct {
	ID     string
	Status string // OK, MISSING, TODO or FAIL
	Why    string
}

// Line renders the result as an output line.
func (r Result) Line() string {
	if r.Status == "OK" {
		return "OK " + r.ID
	}
	return fmt.Sprintf("%s %s: %s", r.Status, r.ID, r.Why)
}

var resultLineRe = regexp.MustCompile(`^\s*[-*]\s+(CL-[0-9]{2,3})\b.*\|\s*result:\s*(.*?)\s*$`)

// CheckBody verifies that the block in a PR body holds a result for every matched item.
func CheckBody(matched []Item, body string) []Result {
	results := map[string]string{}
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "## Diff checklist" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(t, "</details>") {
			break
		}
		if m := resultLineRe.FindStringSubmatch(line); m != nil {
			if _, dup := results[m[1]]; !dup {
				results[m[1]] = m[2]
			}
		}
	}
	out := make([]Result, 0, len(matched))
	for _, it := range matched {
		res, ok := results[it.ID]
		out = append(out, judge(it.ID, res, ok))
	}
	return out
}

func judge(id, res string, found bool) Result {
	if !found {
		return Result{id, "MISSING", "no `- " + id + " ... | result: ...` line in the Diff checklist block"}
	}
	word, rest, _ := strings.Cut(res, " ")
	rest = strings.TrimSpace(rest)
	switch word {
	case "PASS":
		return Result{ID: id, Status: "OK"}
	case "FAIL":
		if rest == "" {
			rest = "no evidence given"
		}
		return Result{id, "FAIL", rest}
	case "N/A":
		if rest == "" {
			return Result{id, "TODO", "N/A needs a reason"}
		}
		return Result{ID: id, Status: "OK"}
	case "TODO":
		return Result{id, "TODO", "replace TODO with PASS <evidence>, FAIL <evidence> or N/A <reason>"}
	}
	return Result{id, "TODO", "result must start with PASS, FAIL or N/A"}
}
