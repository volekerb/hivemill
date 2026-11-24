package obs

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/kit/spend"
)

// Retention is how far back ingest reads and keeps requests and tool calls.
const Retention = 90 * 24 * time.Hour

// IngestOptions are the inputs of one ingest run.
type IngestOptions struct {
	Paths Paths
	Root  string    // Claude Code projects dir (default ~/.claude/projects)
	Now   time.Time // injected clock
	// FlowEvery is how often pr-flow snapshots are refreshed; 0 means never in this run.
	FlowEvery time.Duration
}

// IngestStats says what one run did.
type IngestStats struct {
	Files, FilesRead, Requests, ToolCalls, Sessions, Pruned int
	BytesRead                                               int64
	Flow                                                    string // refreshed | fresh | skipped | error: ...
	Busy                                                    bool
}

// ErrBusy means another ingest holds the lock.
var ErrBusy = errors.New("obs: another ingest is running")

// Ingest updates obs.db from the transcripts under o.Root, re-attributes every session and, when due, refreshes the
// pr-flow snapshots. It is incremental: each transcript is read from where the last run stopped.
func Ingest(o IngestOptions) (*IngestStats, error) {
	st := &IngestStats{}
	if err := os.MkdirAll(filepath.Dir(o.Paths.Lock), 0o755); err != nil {
		return nil, fmt.Errorf("obs: create %s: %w", filepath.Dir(o.Paths.Lock), err)
	}
	lk, err := os.OpenFile(o.Paths.Lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("obs: open lock %s: %w", o.Paths.Lock, err)
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		st.Busy = true
		return st, ErrBusy
	}
	db, err := openStore(o.Paths.Store)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	cutoff := o.Now.Add(-Retention)
	if err := scanRoot(db, o.Root, cutoff, st); err != nil {
		return nil, err
	}
	n, err := prune(db, cutoff)
	if err != nil {
		return nil, err
	}
	st.Pruned = n
	if st.Sessions, err = attribute(db, o.Paths.FactoryDB, o.Now); err != nil {
		return nil, err
	}
	st.Flow = refreshFlow(db, o)
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES('ingested_ms', ?)`, o.Now.UnixMilli()); err != nil {
		return nil, fmt.Errorf("obs: write ingest time: %w", err)
	}
	return st, nil
}

// fileState is one transcript's row in files.
type fileState struct {
	path, session, parent                         string
	ino, size, mtime, off                         int64
	role, kickoffAgent, agentName, wtGoal, wtTask string
	usersSeen                                     int
	firstDone                                     bool
	known                                         bool
}

func scanRoot(db *sql.DB, root string, cutoff time.Time, st *IngestStats) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("obs: walk %s: %w", root, err)
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		st.Files++
		rel, _ := filepath.Rel(root, path)
		fsx, err := loadFile(db, path)
		if err != nil {
			return err
		}
		ino := int64(0)
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			ino = int64(sys.Ino)
		}
		if !fsx.known {
			fsx.session = strings.TrimSuffix(filepath.Base(path), ".jsonl")
			// Subagent transcripts live in <project>/<parent session>/subagents/agent-*.jsonl.
			if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) == 4 && parts[2] == "subagents" {
				fsx.parent = parts[1]
			}
			if m := taskRe.FindStringSubmatch(strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]); m != nil {
				fsx.wtGoal, fsx.wtTask = m[1], m[2]
			}
		}
		if fsx.known && (fsx.ino != ino || info.Size() < fsx.off) {
			fsx.off = 0 // replaced or truncated: read again; requests and tool calls dedupe by key
		}
		fsx.ino, fsx.size, fsx.mtime = ino, info.Size(), info.ModTime().UnixMilli()
		if fsx.known && fsx.off == fsx.size {
			return nil
		}
		st.FilesRead++
		return readTranscript(db, fsx, cutoff, st)
	})
}

func loadFile(db *sql.DB, path string) (*fileState, error) {
	f := &fileState{path: path}
	var parent sql.NullString
	var first int
	err := db.QueryRow(`SELECT ino, size, mtime_ms, off, session, parent, role, kickoff_agent, agent_name, wt_goal,
		wt_task, users_seen, first_done FROM files WHERE path=?`, path).Scan(&f.ino, &f.size, &f.mtime, &f.off,
		&f.session, &parent, &f.role, &f.kickoffAgent, &f.agentName, &f.wtGoal, &f.wtTask, &f.usersSeen, &first)
	if errors.Is(err, sql.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obs: read file state: %w", err)
	}
	f.known, f.parent, f.firstDone = true, parent.String, first == 1
	return f, nil
}

var (
	userKey      = []byte(`"type":"user"`)
	assistantKey = []byte(`"type":"assistant"`)
	agentNameKey = []byte(`"agent-name"`)
	toolKey      = []byte(`"tool_`)
	cwdKey       = []byte(`"cwd"`)
)

// readTranscript reads one transcript from its stored offset to the last complete line, in one transaction.
func readTranscript(db *sql.DB, f *fileState, cutoff time.Time, st *IngestStats) error {
	fh, err := os.Open(f.path)
	if err != nil {
		return nil // vanished or unreadable: skipped, as hm-spend does
	}
	defer fh.Close()
	if _, err := fh.Seek(f.off, io.SeekStart); err != nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("obs: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a committed tx makes this a no-op
	insReq, err := tx.Prepare(`INSERT OR IGNORE INTO requests(key, session, ts_ms, model, speed, input, output,
		cache_read, cache_write_5m, cache_write_1h, priced, cost, cache_write_cost, spawn, polling, work)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("obs: prepare: %w", err)
	}
	defer insReq.Close()
	insTool, err := tx.Prepare(`INSERT OR IGNORE INTO tool_calls(id, session, ts_ms, tool, skill, head) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("obs: prepare: %w", err)
	}
	defer insTool.Close()
	setResult, err := tx.Prepare(`UPDATE tool_calls SET result_bytes=? WHERE id=?`)
	if err != nil {
		return fmt.Errorf("obs: prepare: %w", err)
	}
	defer setResult.Close()

	br := bufio.NewReaderSize(fh, 1<<20)
	var lastTS time.Time
	for {
		raw, rerr := br.ReadBytes('\n')
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			break // EOF, or a line still being written: the next run reads it whole
		}
		f.off += int64(len(raw))
		st.BytesRead += int64(len(raw))
		if r, key, ok := spend.ParseRequest(raw); ok {
			lastTS = r.At
			if !r.At.Before(cutoff) {
				r.First = !f.firstDone
				cost, cw, spawn, polling, work, priced := spend.Buckets(r)
				res, err := insReq.Exec(key, f.session, r.At.UnixMilli(), safeName(r.Model), r.Speed, r.Usage.Input,
					r.Usage.Output, r.Usage.CacheRead, r.Usage.CacheWrite5m, r.Usage.CacheWrite1h, b2i(priced), cost, cw,
					spawn, polling, work)
				if err != nil {
					return fmt.Errorf("obs: insert request: %w", err)
				}
				if n, _ := res.RowsAffected(); n == 1 {
					f.firstDone = true
					st.Requests++
				}
			} else if !f.firstDone {
				f.firstDone = true // hm-spend's first request of this file is older than the store keeps
			}
		}
		if err := readContent(raw, f, lastTS, cutoff, insTool, setResult, st); err != nil {
			return err
		}
		if rerr != nil {
			break
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO files(path, ino, size, mtime_ms, off, session, parent, role,
		kickoff_agent, agent_name, wt_goal, wt_task, users_seen, first_done) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.path, f.ino, f.size, f.mtime, f.off, f.session, nullable(f.parent), f.role, f.kickoffAgent, f.agentName,
		f.wtGoal, f.wtTask, f.usersSeen, b2i(f.firstDone)); err != nil {
		return fmt.Errorf("obs: write file state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("obs: commit: %w", err)
	}
	return nil
}

// readContent takes the role and agent name from the kickoff, the agent-name line and the worktree in the cwd, and
// the tool calls and result sizes, from one line. The text it reads is never stored.
func readContent(raw []byte, f *fileState, ts, cutoff time.Time, insTool, setResult *sql.Stmt, st *IngestStats) error {
	isUser := bytes.Contains(raw, userKey)
	wantKick := isUser && f.role == "" && f.usersSeen < spend.MaxKickoffLines
	if wantKick {
		if text, ok := spend.UserText(raw); ok {
			f.usersSeen++
			f.role = spend.RoleFromKickoff(text) // same rule as hm-spend report
			if f.kickoffAgent == "" {
				if m := kickoffAgentRe.FindStringSubmatch(text); m != nil {
					f.kickoffAgent = m[1]
				}
			}
		}
	}
	needCwd := f.wtTask == "" && bytes.Contains(raw, cwdKey)
	needName := f.agentName == "" && bytes.Contains(raw, agentNameKey)
	needTools := bytes.Contains(raw, toolKey) && (isUser || bytes.Contains(raw, assistantKey))
	if !needCwd && !needName && !needTools {
		return nil
	}
	var l contentLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	if needCwd {
		if m := taskRe.FindStringSubmatch(l.Cwd); m != nil {
			f.wtGoal, f.wtTask = m[1], m[2]
		}
	}
	if needName && l.Type == "agent-name" && nameRe.MatchString(l.AgentName) {
		f.agentName = l.AgentName
	}
	if !needTools {
		return nil
	}
	uses, results := tools(&l)
	if len(uses) > 0 && !ts.IsZero() && !ts.Before(cutoff) {
		for _, u := range uses {
			res, err := insTool.Exec(u.ID, f.session, ts.UnixMilli(), u.Tool, u.Skill, u.Head)
			if err != nil {
				return fmt.Errorf("obs: insert tool call: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				st.ToolCalls++
			}
		}
	}
	for _, r := range results {
		if _, err := setResult.Exec(r.Bytes, r.ID); err != nil {
			return fmt.Errorf("obs: write tool result size: %w", err)
		}
	}
	return nil
}

func prune(db *sql.DB, cutoff time.Time) (int, error) {
	res, err := db.Exec(`DELETE FROM requests WHERE ts_ms < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("obs: prune requests: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := db.Exec(`DELETE FROM tool_calls WHERE ts_ms < ?`, cutoff.UnixMilli()); err != nil {
		return 0, fmt.Errorf("obs: prune tool calls: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM files WHERE mtime_ms < ?`, cutoff.UnixMilli()); err != nil {
		return 0, fmt.Errorf("obs: prune files: %w", err)
	}
	return int(n), nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
