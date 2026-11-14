package data

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AllowedAdvisorModels and MaxTS are the constants of web/advisor_audit.py.
var AllowedAdvisorModels = []string{"claude-opus-5-5", "claude-sonnet-5-5"}

// MaxTS is how many timestamps are listed per session; the count is always exact.
const MaxTS = 20

var agentTaskRe = regexp.MustCompile(`(G-\d+-T-\d+)$`)

// AuditProjectsDir is advisor_audit.projects_dir: $FACTORY_CLAUDE_PROJECTS, else ~/.claude/projects.
func AuditProjectsDir() string { return ProjectsDir() }

// AgentOf is advisor_audit.agent_of: the agent label from the transcript path, the project dir name (a factory
// worktree dir ends in G-nnn-T-nnnn).
func AgentOf(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "?"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	proj := ""
	if len(parts) > 1 {
		proj = parts[0]
	}
	if m := agentTaskRe.FindStringSubmatch(proj); m != nil {
		return m[1]
	}
	if p := strings.TrimLeft(proj, "-"); p != "" {
		return p
	}
	return "?"
}

func advisorModels(node any, out map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		if t, ok := n["type"].(string); ok && strings.HasPrefix(t, "advisor_") {
			if m, ok := n["model"].(string); ok {
				out[m] = true
			}
		}
		if m, ok := n["advisorModel"].(string); ok && m != "" {
			out[m] = true
		}
		for _, v := range n {
			advisorModels(v, out)
		}
	case []any:
		for _, v := range n {
			advisorModels(v, out)
		}
	}
}

// ScanResult is advisor_audit.scan_file's result.
type ScanResult struct {
	AdvisorCalls  []string
	Fable         []string
	AdvisorModels map[string]bool
}

// ScanFile is advisor_audit.scan_file: only names, ids and timestamps are read out of a transcript.
func ScanFile(path string) (*ScanResult, error) {
	res := &ScanResult{AdvisorModels: map[string]bool{}}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			scanAuditLine(line, res)
		}
		if rerr == io.EOF {
			return res, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

func scanAuditLine(line []byte, res *ScanResult) {
	low := bytes.ToLower(line)
	if !bytes.Contains(low, []byte("advisor")) && !bytes.Contains(low, []byte("fable")) {
		return // cheap prefilter: most lines never reach the JSON decoder
	}
	var obj map[string]any
	if json.Unmarshal(bytes.TrimSpace(line), &obj) != nil || obj == nil {
		return
	}
	ts, _ := obj["timestamp"].(string)
	advisorModels(obj, res.AdvisorModels)
	msg, _ := obj["message"].(map[string]any)
	if obj["type"] != "assistant" || msg == nil {
		return
	}
	if m, _ := msg["model"].(string); strings.Contains(strings.ToLower(m), "fable") {
		res.Fable = append(res.Fable, ts)
	}
	content, _ := msg["content"].([]any)
	for _, b := range content {
		blk, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := blk["type"].(string); (t == "tool_use" || t == "server_tool_use") && blk["name"] == "advisor" {
			res.AdvisorCalls = append(res.AdvisorCalls, ts)
		}
	}
}

// AuditRow is one session of Audit.
type AuditRow struct {
	Agent                string   `json:"agent"`
	Session              string   `json:"session"`
	AdvisorCalls         int      `json:"advisor_calls"`
	AdvisorCallTS        []string `json:"advisor_call_ts"`
	FableMessages        int      `json:"fable_messages"`
	FableTS              []string `json:"fable_ts"`
	AdvisorModels        []string `json:"advisor_models"`
	FlaggedAdvisorModels []string `json:"flagged_advisor_models"`
}

func headTS(ts []string) []string {
	s := append([]string{}, ts...)
	sort.Strings(s)
	if len(s) > MaxTS {
		s = s[:MaxTS]
	}
	return s
}

// Audit is advisor_audit.audit: the per-session rows that have an advisor call, a fable model or a flagged advisor
// model, sorted by transcript path.
func Audit(root string) ([]AuditRow, error) {
	rows := []AuditRow{}
	for _, p := range jsonlFiles(root) {
		r, err := ScanFile(p)
		if err != nil {
			return nil, err
		}
		models := []string{}
		for m := range r.AdvisorModels {
			models = append(models, m)
		}
		sort.Strings(models)
		bad := []string{}
		for _, m := range models {
			if !contains(AllowedAdvisorModels, m) {
				bad = append(bad, m)
			}
		}
		if len(r.AdvisorCalls) == 0 && len(r.Fable) == 0 && len(bad) == 0 {
			continue
		}
		rows = append(rows, AuditRow{AgentOf(p, root), strings.TrimSuffix(filepath.Base(p), ".jsonl"),
			len(r.AdvisorCalls), headTS(r.AdvisorCalls), len(r.Fable), headTS(r.Fable), models, bad})
	}
	return rows, nil
}

// RenderAudit is advisor_audit.render.
func RenderAudit(rows []AuditRow) string {
	if len(rows) == 0 {
		return "no advisor calls, no fable model ids, no flagged advisor models"
	}
	var out []string
	var calls, fable int
	for _, r := range rows {
		models := strings.Join(r.AdvisorModels, ",")
		if models == "" {
			models = "-"
		}
		line := fmt.Sprintf("%s session=%s advisor_calls=%d fable_messages=%d advisor_models=%s", r.Agent, r.Session, r.AdvisorCalls, r.FableMessages, models)
		if len(r.FlaggedAdvisorModels) > 0 {
			line += " FLAGGED=" + strings.Join(r.FlaggedAdvisorModels, ",")
		}
		out = append(out, line)
		if len(r.AdvisorCallTS) > 0 {
			out = append(out, "    advisor at: "+strings.Join(r.AdvisorCallTS, " "))
		}
		if len(r.FableTS) > 0 {
			out = append(out, "    fable at: "+strings.Join(r.FableTS, " "))
		}
		calls += r.AdvisorCalls
		fable += r.FableMessages
	}
	out = append(out, fmt.Sprintf("total: %d sessions, advisor_calls=%d, fable_messages=%d", len(rows), calls, fable))
	return strings.Join(out, "\n")
}

// RunAdvisorAudit is advisor_audit.main: `[--projects DIR] [--json]`. It returns 2 when the transcript directory is missing.
func RunAdvisorAudit(args []string, stdout, stderr io.Writer) int {
	root := ""
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--projects":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "advisor_audit: --projects needs a directory")
				return 2
			}
			i++
			root = args[i]
		default:
			fmt.Fprintf(stderr, "advisor_audit: unrecognized argument %s\n", args[i])
			return 2
		}
	}
	if root == "" {
		root = AuditProjectsDir()
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "transcript directory not found: %s (pass --projects DIR)\n", root)
		return 2
	}
	rows, err := Audit(root)
	if err != nil {
		fmt.Fprintf(stderr, "advisor_audit: %v\n", err)
		return 2
	}
	if asJSON {
		b, err := DumpsIndent1(rows)
		if err != nil {
			fmt.Fprintf(stderr, "advisor_audit: %v\n", err)
			return 2
		}
		stdout.Write(append(b, '\n'))
	} else {
		fmt.Fprintln(stdout, RenderAudit(rows))
	}
	return 0
}
