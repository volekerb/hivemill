package hm

// A clone-mode session writes its transcript on its own machine. The costs (task budget, session.ended, report, obs)
// are read from transcripts under the coordinator's ~/.claude/projects, so the runner copies them there.

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// syncActiveTranscripts copies the transcripts of a running clone-mode run, so the spend caps see its cost.
func (r *Runner) syncActiveTranscripts(run *Run) {
	if !run.cloned() {
		return
	}
	sess, err := r.Store.Sessions(run.Task)
	if err != nil {
		r.printf("%s: transcripts: %v\n", run.Task, err)
		return
	}
	var ids []string
	for _, s := range sess {
		if s.Started >= run.Dispatched {
			ids = append(ids, s.SID)
		}
	}
	r.syncTranscripts(run, ids)
}

// syncTranscripts copies the run's transcripts that grew since the last copy. An unreachable machine leaves the copy as
// it was: the next pass reads it again.
func (r *Runner) syncTranscripts(run *Run, ids []string) {
	if !run.cloned() {
		return
	}
	var want []string
	for _, id := range ids {
		if sessionIDRe.MatchString(id) {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return
	}
	root := filepath.Join(r.Env.UserHome, ".claude", "projects")
	const cd = `cd "$HOME/.claude/projects" 2>/dev/null || exit 0; `
	out, err := r.rshOK(run.Machine, "", 60*time.Second, cd+"for s in "+strings.Join(want, " ")+`; do
for p in */"$s".jsonl */"$s"/subagents/*.jsonl */"$s"/subagents/workflows/*/*.jsonl; do
[ -f "$p" ] && printf '%s %s\n' "$(wc -c < "$p" | tr -d ' ')" "$p"
done; done; true`)
	if err != nil {
		r.printf("%s: transcripts: %v\n", run.Task, err)
		return
	}
	var grown []string
	for _, line := range strings.Split(out, "\n") {
		size, name, ok := strings.Cut(line, " ")
		n, err := strconv.ParseInt(size, 10, 64)
		if !ok || err != nil || !transcriptOf(name, want) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, name)); err != nil || fi.Size() < n {
			grown = append(grown, name)
		}
	}
	if len(grown) == 0 {
		return
	}
	// tar exits 1 when a file grew while it was read, which a running session does: the archive is still usable.
	rc, archive, errs := r.rsh(run.Machine, "./"+strings.Join(grown, "\n./")+"\n", 5*time.Minute, cd+"COPYFILE_DISABLE=1 exec tar -cf - -T -")
	if rc != 0 && rc != 1 {
		err = fmt.Errorf("ssh %s: exit %d: %s", run.Machine, rc, firstLine(errs))
	} else {
		err = unpackTranscripts(root, archive, grown)
	}
	if err != nil {
		r.printf("%s: transcripts: %v\n", run.Task, err)
	}
}

// transcriptOf: name is a transcript of one of the sessions, at a path under the projects directory.
func transcriptOf(name string, sessions []string) bool {
	parts := strings.Split(name, "/")
	if !filepath.IsLocal(name) || filepath.Clean(name) != name || len(parts) < 2 || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	if len(parts) == 2 {
		return slices.Contains(sessions, strings.TrimSuffix(parts[1], ".jsonl"))
	}
	return slices.Contains(sessions, parts[1]) && parts[2] == "subagents"
}

// unpackTranscripts writes the regular files of the archive that were asked for, and nothing else.
func unpackTranscripts(root, archive string, asked []string) error {
	tr := tar.NewReader(strings.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./") // asked for as ./name: a project directory starts with "-"
		if h.Typeflag != tar.TypeReg || !slices.Contains(asked, name) {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := writeFile(dst, string(data), 0o600); err != nil {
			return err
		}
	}
}
