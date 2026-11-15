package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// after_merge (judge post-merge hook): the lesson links are stamped in judgeLessons; this part tells the director (and,
// when the director itself is stale, the boss) that role files changed. Same order of statements and gh calls as Python:
// lessons, then `gh pr view --json files`, then the notices.

// judgeRoles is ROLES.
var judgeRoles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner",
	"staff-engineer", "researcher", "scout", "retro", "evaluator"}

var (
	judgeRoleFile   = regexp.MustCompile(`^roles/(?:settings/)?([a-z-]+)\.(?:md|json\.in|json)$`)
	judgeRolePlugin = regexp.MustCompile(`^roles/plugins/([a-z-]+)/`)
)

// judgeRolesAffected is _roles_affected: the persistent roles (boss, director, lead) whose prompt the paths change.
func judgeRolesAffected(paths []string) map[string]bool {
	out := map[string]bool{}
	var rolePaths []string
	for _, f := range paths {
		if strings.HasPrefix(f, "roles/") {
			rolePaths = append(rolePaths, f)
		}
	}
	hookOnly := len(rolePaths) > 0
	for _, f := range rolePaths {
		hookOnly = hookOnly && strings.HasPrefix(f, "roles/hooks/")
	}
	if hookOnly {
		return out // a hook-only PR: per-call scripts run from disk, no pane needs a relaunch
	}
	for _, f := range paths {
		if f == "roles/_common.md" || strings.HasPrefix(f, "roles/hooks/") {
			for _, r := range []string{"boss", "director", "lead"} {
				out[r] = true
			}
			continue
		}
		m := judgeRoleFile.FindStringSubmatch(f)
		if m == nil {
			m = judgeRolePlugin.FindStringSubmatch(f)
		}
		if m != nil && inList(judgeRoles, m[1]) {
			out[m[1]] = true
		}
	}
	for r := range out {
		if r != "boss" && r != "director" && r != "lead" {
			delete(out, r)
		}
	}
	return out
}

// judgeAfterMerge runs after the merge is recorded; the caller turns an error into a post-merge-hook-failed event.
func judgeAfterMerge(c *ctx, j *judgeEnv, t judgeTask) error {
	if err := judgeLessons(c, t.id, t.prURL); err != nil {
		return err
	}
	out, err := j.ghJSONRaw("pr", "view", t.prURL, "--json", "files")
	if err != nil {
		var gf *ghFail
		if errors.As(err, &gf) {
			return gf // SystemExit(msg): str(e) is the message
		}
		return fmt.Errorf("gh pr view %s: could not run", t.prURL)
	}
	var v struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return fmt.Errorf("gh pr view files: %w", err)
	}
	files := make([]string, len(v.Files))
	for i, f := range v.Files {
		files[i] = f.Path
	}
	affected := judgeRolesAffected(files)
	if len(affected) == 0 {
		return nil
	}
	roles := make([]string, 0, len(affected))
	for r := range affected {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	args := make([]any, len(roles))
	for i, r := range roles {
		args[i] = r
	}
	rows, err := c.query("SELECT name, role FROM agents WHERE status='live' AND role IN ("+strings.TrimSuffix(strings.Repeat("?,", len(roles)), ",")+") ORDER BY name", args...)
	if err != nil {
		return err
	}
	type agent struct{ name, role string }
	var live []agent
	for rows.Next() {
		var a agent
		if err := rows.Scan(&a.name, &a.role); err != nil {
			rows.Close()
			return err
		}
		live = append(live, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}
	var changed []string
	for _, f := range files {
		if strings.HasPrefix(f, "roles/") {
			changed = append(changed, f)
		}
	}
	cmds := make([]string, len(live))
	names := make([]string, len(live))
	directorStale := false
	for i, a := range live {
		cmds[i] = fmt.Sprintf("- %s (%s): factory relaunch %s --when-idle", a.name, a.role, a.name)
		names[i] = a.name
		directorStale = directorStale || a.role == "director"
	}
	if err := judgeCollapseNotice(c, "director", "role files changed: stale agents", []string{t.prURL}, changed, cmds,
		"Live agents above have a stale prompt.", t.id); err != nil {
		return err
	}
	if directorStale {
		if err := judgeCollapseNotice(c, "boss", "role files changed: director stale", []string{t.prURL}, changed, cmds,
			"Run the director's relaunch line above; the director handles the other agents.", t.id); err != nil {
			return err
		}
	}
	return event(c, "task", t.id, "roles-changed", strp(strings.Join(names, ",")))
}

func judgeIsWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }

// judgeOldHeader is re.match(r"T-\d+ merged \((\S+)\) and changed role files:$", ln) with Python's Unicode \d and \S
// spelled out (unicode.Nd; any character but Python whitespace).
func judgeOldHeader(ln string) (string, bool) {
	rest, ok := strings.CutPrefix(ln, "T-")
	if !ok {
		return "", false
	}
	n := 0
	for _, r := range rest {
		if !unicode.Is(unicode.Nd, r) {
			break
		}
		n += len(string(r))
	}
	if n == 0 {
		return "", false
	}
	rest, ok = strings.CutPrefix(rest[n:], " merged (")
	if !ok {
		return "", false
	}
	const tail = ") and changed role files:"
	if !strings.HasSuffix(rest, tail) {
		return "", false
	}
	pr := strings.TrimSuffix(rest, tail)
	if pr == "" || strings.IndexFunc(pr, pyIsSpace) >= 0 {
		return "", false
	}
	return pr, true
}

// judgeAgentKey is re.sub(r" \(\w[\w-]*\)$", "", c.split(":")[0]) with \w spelled out.
func judgeAgentKey(c string) string {
	k, _, _ := strings.Cut(c, ":")
	if !strings.HasSuffix(k, ")") {
		return k
	}
	i := strings.LastIndex(k, " (")
	if i < 0 {
		return k
	}
	rs := []rune(k[i+2 : len(k)-1])
	if len(rs) == 0 || !judgeIsWord(rs[0]) {
		return k
	}
	for _, r := range rs[1:] {
		if !judgeIsWord(r) && r != '-' {
			return k
		}
	}
	return k[:i]
}

// judgeCollapseNotice is _collapse_notice: send a factory request notice, or fold it into the recipient's still-unread
// one with the same subject (PRs and command lines unioned, files sorted, created bumped).
func judgeCollapseNotice(c *ctx, to, subject string, prs, files, cmds []string, tail, task string) error {
	rcpt, err := msgResolveRecipient(c, to, "")
	if err != nil {
		return err
	}
	var oldID int64
	var oldBody sql.NullString
	err = c.db.QueryRow("SELECT id, body FROM messages WHERE recipient=? AND sender='factory' AND subject=? AND read_at IS NULL ORDER BY id DESC LIMIT 1",
		rcpt, subject).Scan(&oldID, &oldBody)
	have := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if have {
		sect := ""
		var prior []string
		for _, ln := range pySplitlines(oldBody.String) {
			hdr, isHdr := judgeOldHeader(ln)
			switch {
			case strings.HasPrefix(ln, "PR: "):
				prior = append(prior, ln[4:])
			case isHdr:
				prior = append(prior, hdr)
				sect = "files"
			case strings.HasPrefix(ln, "Changed files:"):
				sect = "files"
			case strings.HasPrefix(ln, "Live agents:") || strings.HasPrefix(ln, "Live agents with a stale prompt:"):
				sect = "cmds"
			case strings.HasPrefix(ln, "- ") && sect == "files":
				files = append(files, ln[2:])
			case strings.HasPrefix(ln, "- ") && sect == "cmds":
				cmds = append(cmds, ln)
			}
		}
		merged := append([]string{}, prior...)
		for _, p := range prs {
			if !inList(prior, p) {
				merged = append(merged, p)
			}
		}
		prs = merged
	}
	byAgent := map[string]string{}
	for _, cm := range cmds {
		byAgent[judgeAgentKey(cm)] = cm
	}
	uniq := make([]string, 0, len(byAgent))
	for _, cm := range byAgent {
		uniq = append(uniq, cm)
	}
	sort.Strings(uniq)
	seen := map[string]bool{}
	var sorted []string
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			sorted = append(sorted, f)
		}
	}
	sort.Strings(sorted)
	prLines := make([]string, len(prs))
	for i, p := range prs {
		prLines[i] = "PR: " + p
	}
	fileLines := make([]string, len(sorted))
	for i, f := range sorted {
		fileLines[i] = "- " + f
	}
	body := strings.Join(prLines, "\n") + "\nChanged files:\n" + strings.Join(fileLines, "\n")
	if len(uniq) > 0 {
		body += "\nLive agents:\n" + strings.Join(uniq, "\n")
	}
	body += "\n" + tail
	if have {
		_, err := c.exec("UPDATE messages SET body=?, task_id=?, created=? WHERE id=?", body, task, eventTS(c), oldID)
		return err
	}
	// send(con, to, ...) resolves the already resolved name once more, in the message's org (task, then sender "factory")
	org, err := orgFor(c, "", task, "factory", "")
	if err != nil {
		return err
	}
	to2, err := msgResolveRecipient(c, rcpt, org)
	if err != nil {
		return err
	}
	_, err = c.exec("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		"factory", to2, "request", subject, body, nil, task, eventTS(c))
	return err
}
