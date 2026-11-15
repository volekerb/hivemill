// Package skilleval is the Go port of scout/skilleval.py: collecting a finished skill trial (end state, contained
// checker, metrics, cleanup), the trial queue, cancel, void, the results text and the tick pass (docs/scout-eval.md).
//
// SECURITY CODE. The evaluator wrote work/ and the transcripts, so both are untrusted. check.py and the hidden tests
// run agent-written code: they run ONLY through sandbox.RunContained (no network, no token, ~/.claude and ~/factory
// hidden), never in this process and never outside that wrapper. Without user namespaces the trial is recorded as
// `error`; there is no uncontained fallback (TestSkillevalRequiresSandbox). Nothing here executes third-party files.
package skilleval

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // registers the driver temp-DB reads use

	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
)

// Constants carried over from scout/skilleval.py.
const (
	OutputMax    = 2048      // OUTPUT_MAX
	StartTail    = 20        // START_TAIL
	StartingMax  = 15        // STARTING_MAX_MIN
	StartLogRead = 64 * 1024 // the pread cap of start_log_lines
	MarkerRead   = 256       // the read cap of control_marker
	// MaxTranscriptLine is a safety cap the Python code does not have: one transcript line the evaluator wrote can
	// be any size, and a longer one is skipped instead of read into memory.
	MaxTranscriptLine = 64 << 20

	ControlPlugin = "eval-control-marker"
	MarkerFile    = "SKILL_MARKER.txt"
	MarkerText    = "eval-marker-loaded"
	BaselineID    = "C-0000"
	BaselineSha   = "baseline"
)

// CheckTimeout is CHECK_TIMEOUT_S; a variable only so a test can shorten it.
var CheckTimeout = 60 * time.Second

var (
	Reasons      = []string{"timeout", "over-budget", "cancelled"}
	Terminal     = []string{"passed", "failed", "timeout", "over-budget", "error", "cancelled", "voided"}
	Voidable     = []string{"passed", "failed", "timeout", "over-budget", "error"}
	DeleteSandbx = []string{"passed", "failed", "cancelled"} // error / timeout / over-budget are kept for the cleanup
)

func in(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// CollectError is "the trial cannot be collected now"; the CLI maps it to exit 2.
type CollectError struct{ Msg string }

func (e *CollectError) Error() string { return e.Msg }

func cerr(format string, a ...any) error { return &CollectError{Msg: fmt.Sprintf(format, a...)} }

// SpawnRefused is returned by API.Launch when the spawner refused (Python SystemExit): launch already set the row.
type SpawnRefused struct{ Code int }

func (e *SpawnRefused) Error() string { return fmt.Sprintf("spawn refused (exit %d)", e.Code) }

// API is what the CLI hands the collect/tick code (Python's `api` object): the clock, the config, force_stop and the
// trial launcher.
type API interface {
	Now() string
	Clock() time.Time
	Cfg(db *sql.DB, key string) (string, error)
	Event(db *sql.DB, entity, entityID, action, detail string) error
	// ForceStop stops an agent; attemptSeen is the stop_attempt_at the caller read (so a retry re-claims a failed stop).
	ForceStop(db *sql.DB, name, mode string, attemptSeen sql.NullString) (res, note string, err error)
	// Launch starts one claimed trial. It returns *SpawnRefused when the spawn was refused.
	Launch(db *sql.DB, t Trial, root, home, unshare string) error
	Home() string    // the passwd home (or the test seam): where the eval token lives
	HomeDir() string // HOME, the factory home used to resolve skilleval_root
	Unshare() string
}

// Trial is one skill_trials row.
type Trial struct {
	ID                                                                      int64
	CandidateID, VendorSha, Task, Condition, Status                         string
	Agent, Sandbox                                                          sql.NullString
	Queued                                                                  string
	Started, Ended                                                          sql.NullString
	Passed, WallS, TokensIn, TokensOut, CacheRead, CacheWrite, SkillInvoked sql.NullInt64
	CheckOutput, Author, VoidReason, VoidWhy                                sql.NullString
}

const trialCols = "id, candidate_id, vendor_sha, task, condition, status, agent, sandbox, queued, started, ended, passed, " +
	"wall_s, tokens_in, tokens_out, cache_read, cache_write, skill_invoked, check_output, author, void_reason, void_why"

func (t *Trial) dest() []any {
	return []any{&t.ID, &t.CandidateID, &t.VendorSha, &t.Task, &t.Condition, &t.Status, &t.Agent, &t.Sandbox, &t.Queued,
		&t.Started, &t.Ended, &t.Passed, &t.WallS, &t.TokensIn, &t.TokensOut, &t.CacheRead, &t.CacheWrite, &t.SkillInvoked,
		&t.CheckOutput, &t.Author, &t.VoidReason, &t.VoidWhy}
}

// GetTrial loads one trial; ok=false when there is none.
func GetTrial(db *sql.DB, id int64) (Trial, bool, error) {
	var t Trial
	err := db.QueryRow("SELECT "+trialCols+" FROM skill_trials WHERE id=?", id).Scan(t.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return Trial{}, false, nil
	}
	if err != nil {
		return Trial{}, false, fmt.Errorf("load trial %d: %w", id, err)
	}
	return t, true, nil
}

// QueryTrials runs a SELECT over skill_trials (the caller passes the WHERE/ORDER tail).
func QueryTrials(db *sql.DB, tail string, args ...any) ([]Trial, error) {
	rows, err := db.Query("SELECT "+trialCols+" FROM skill_trials "+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("query skill_trials: %w", err)
	}
	defer rows.Close()
	var out []Trial
	for rows.Next() {
		var t Trial
		if err := rows.Scan(t.dest()...); err != nil {
			return nil, fmt.Errorf("scan skill_trials: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read skill_trials: %w", err)
	}
	return out, nil
}

// FhDB is web.dbpath.resolve(fh): <fh>/db/factory.db, else the pre-migration <fh>/factory.db.
func FhDB(fh string) string {
	n, l := filepath.Join(fh, "db", "factory.db"), filepath.Join(fh, "factory.db")
	if _, err := os.Stat(n); err == nil {
		return n
	}
	if _, err := os.Stat(l); err != nil {
		return n
	}
	return l
}

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// pyRepr is repr(str): the quote Python picks, backslash escapes and \x/\u/\U for non-printable characters.
func pyRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// roundEven is Python's round(x) for a float: halves go to the even integer.
func roundEven(x float64) int64 { return int64(math.RoundToEven(x)) }

// wallSeconds is wall_seconds(started, ended): whole seconds, never negative; ok=false when either stamp is unreadable.
func wallSeconds(started, ended string) (int64, bool) {
	a, ok1 := scout.TS(started)
	b, ok2 := scout.TS(ended)
	if !ok1 || !ok2 {
		return 0, false
	}
	w := roundEven(b.Sub(a).Seconds())
	if w < 0 {
		w = 0
	}
	return w, true
}

// TrialDir is <root>/trials/trNNNN, only when it is a real directory under the root (never a symlink); "" otherwise.
func TrialDir(root string, t Trial) string {
	d := filepath.Join(sandbox.Realpath(root), "trials", sandbox.TrialName(int(t.ID)))
	st, err := os.Lstat(d)
	if err != nil || !st.IsDir() {
		return ""
	}
	return d
}

// refusedText is str(SandboxRefused): the message without the sentinel prefix.
func refusedText(err error) string {
	return strings.TrimPrefix(err.Error(), sandbox.ErrSandboxRefused.Error()+": ")
}

func tail(text string, n int) string {
	b := []byte(text)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.ToValidUTF8(string(b), "") // decode("utf-8", "ignore") drops a cut character at the start
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func cell(n sql.NullInt64) string {
	if !n.Valid {
		return "-"
	}
	return fmt.Sprint(n.Int64)
}
