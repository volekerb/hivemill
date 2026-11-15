package merge

import (
	"fmt"
	"strings"
	"time"
)

// Python constants (bin/factory_cli.py): GH_API_TIMEOUT 5031, CARRY_MAX_COMMITS 5032, LANDING_MAX_COMMITS 5539,
// head_has_main's gh api timeout 5357+.
const (
	GHAPITimeout      = 60 * time.Second
	CarryMaxCommits   = 20
	LandingMaxCommits = 300
	GHCommitsTimeout  = 60 * time.Second
)

// HardenedGitFlags is HARDENED_GIT_FLAGS: put in front of every git call of the carry, staleness and landing checks.
var HardenedGitFlags = []string{"-c", "core.commitGraph=false", "-c", "core.attributesFile=/dev/null"}

// Runner is the process boundary of the carry, staleness and landing checks.
type Runner interface {
	// Git runs `git <HardenedGitFlags> args...` in the task's repo with the carry env (no replace refs, no grafts, no
	// attributes) and returns _git_rc's (exit code, trimmed stdout, trimmed stderr); a timeout is 124.
	Git(args ...string) (rc int, out, errText string)
	// MainSHA runs `gh api repos/<ownerRepo>/commits/main --jq .sha`. err is a start failure or a timeout; its text is
	// what Python prints for it.
	MainSHA(ownerRepo string, timeout time.Duration) (stdout, stderr string, rc int, err error)
	// IsTimeout says whether err from MainSHA is subprocess.TimeoutExpired.
	IsTimeout(err error) bool
}

func orRC(errText string, rc int) string {
	if errText != "" {
		return errText
	}
	return fmt.Sprint(rc)
}

func short(s string) string {
	if r := []rune(s); len(r) > 8 {
		return string(r[:8])
	}
	return s
}

func shortOrQ(s string) string {
	if s == "" {
		return "?"
	}
	return short(s)
}

// Carry is verdict_carries' answer.
type Carry struct {
	OK      bool
	Why     string
	MainSHA string
	Merges  []string
}

// VerdictCarries is verdict_carries: does a PASS recorded at head x still count for head y? Yes when every commit on
// the first-parent path y..x is a pure main merge. An error is a git or gh that could not start (Python raises).
func VerdictCarries(r Runner, x, y string, prNumber int64, repoURL, ownerRepo string) (Carry, error) {
	mainRef, prRef := "refs/factory-judge/main", fmt.Sprintf("refs/factory-judge/pr-%d", prNumber)
	fail := func(f string, a ...any) (Carry, error) { return Carry{Why: fmt.Sprintf(f, a...)}, nil }
	rc, _, e := r.Git("-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head", repoURL, "+refs/heads/main:"+mainRef)
	if rc != 0 {
		return fail("cannot verify, fetch of main failed: %s", orRC(e, rc))
	}
	rc, _, e = r.Git("-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head", repoURL,
		fmt.Sprintf("+refs/pull/%d/head:%s", prNumber, prRef))
	if rc != 0 {
		return fail("cannot verify, git fetch of PR head failed: %s", orRC(e, rc))
	}
	rc, mainSHA, e := r.Git("rev-parse", "--verify", mainRef)
	if rc != 0 {
		return fail("cannot verify, origin/main unknown: %s", orRC(e, rc))
	}
	rc, prSHA, e := r.Git("rev-parse", "--verify", prRef)
	if rc != 0 || prSHA != y {
		return fail("cannot verify, fetched PR head %s is not %s: %s", shortOrQ(prSHA), short(y), orRC(e, rc))
	}
	out, errOut, grc, gerr := r.MainSHA(ownerRepo, GHAPITimeout)
	if gerr != nil {
		if r.IsTimeout(gerr) {
			return fail("cannot verify, gh api for main timed out")
		}
		return Carry{}, gerr
	}
	if got := pyStrip(out); grc != 0 || got != mainSHA {
		shown := short(got)
		if shown == "" {
			shown = pyStrip(errOut)
		}
		if shown == "" {
			shown = fmt.Sprint(grc)
		}
		return fail("cannot verify, fetched main %s does not match GitHub's (%s); is the git remote tampered with?", short(mainSHA), shown)
	}
	cur, merges := y, []string{}
	for i := 0; i < CarryMaxCommits; i++ {
		if cur == x {
			return Carry{OK: true, MainSHA: mainSHA, Merges: merges}, nil
		}
		rc, out, e := r.Git("rev-list", "--parents", "-n", "1", cur)
		if rc != 0 || out == "" {
			return fail("cannot verify, commit %s unreadable: %s", short(cur), orRC(e, rc))
		}
		parts := strings.Fields(out)
		if len(parts) != 3 {
			return fail("not a pure main merge: %s is not a two-parent merge", short(cur))
		}
		p1, p2 := parts[1], parts[2]
		rc, _, e = r.Git("merge-base", "--is-ancestor", p2, mainSHA)
		if rc == 1 {
			return fail("not a pure main merge: second parent of %s is not on origin/main", short(cur))
		}
		if rc != 0 {
			return fail("cannot verify, git error checking second parent of %s: %s", short(cur), orRC(e, rc))
		}
		rc, mt, _ := r.Git("merge-tree", "--write-tree", p1, p2)
		rc2, tree, _ := r.Git("rev-parse", cur+"^{tree}")
		first := mt
		if i := strings.IndexAny(mt, "\n"); i >= 0 {
			first = mt[:i]
		}
		if rc != 0 || rc2 != 0 || mt == "" || first != tree {
			return fail("not a pure main merge: tree differs at %s", short(cur))
		}
		merges = append(merges, cur)
		cur = p1
	}
	if cur == x {
		return Carry{OK: true, MainSHA: mainSHA, Merges: merges}, nil
	}
	return fail("not a pure main merge: %s is not a first-parent ancestor within %d commits", short(x), CarryMaxCommits)
}

// Stale is head_has_main's answer: OK (the head contains main), Why "STALE" (it does not), or a "cannot verify" text.
type Stale struct {
	OK      bool
	Why     string
	MainSHA string
}

// HeadHasMain is head_has_main: is origin/main an ancestor of the PR head `sha`? Anything undecidable is not OK.
func HeadHasMain(r Runner, sha string, prNumber int64, repoURL, ownerRepo string) (Stale, error) {
	mainRef, prRef := "refs/factory-judge/main", fmt.Sprintf("refs/factory-judge/pr-%d", prNumber)
	for _, f := range []struct{ ref, src, what string }{{mainRef, "refs/heads/main", "origin main"},
		{prRef, fmt.Sprintf("refs/pull/%d/head", prNumber), "PR head"}} {
		rc, _, e := r.Git("-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head", repoURL, "+"+f.src+":"+f.ref)
		if rc != 0 {
			return Stale{Why: fmt.Sprintf("git fetch %s failed: %s", f.what, orRC(e, rc))}, nil
		}
	}
	rc, mainSHA, e := r.Git("rev-parse", "--verify", mainRef)
	if rc != 0 {
		return Stale{Why: fmt.Sprintf("origin/main unknown: %s", orRC(e, rc))}, nil
	}
	rc, prSHA, e := r.Git("rev-parse", "--verify", prRef)
	if rc != 0 || prSHA != sha {
		return Stale{Why: fmt.Sprintf("fetched PR head %s is not %s: %s", shortOrQ(prSHA), short(sha), orRC(e, rc))}, nil
	}
	out, errOut, grc, gerr := r.MainSHA(ownerRepo, GHCommitsTimeout)
	if gerr != nil {
		return Stale{Why: "gh api commits/main failed: " + gerr.Error()}, nil
	}
	if got := pyStrip(out); grc != 0 || got != mainSHA {
		shown := short(got)
		if shown == "" {
			shown = pyStrip(errOut)
		}
		if shown == "" {
			shown = fmt.Sprint(grc)
		}
		return Stale{Why: fmt.Sprintf("fetched main %s does not match GitHub's (%s); is the git remote tampered with?", short(mainSHA), shown)}, nil
	}
	rc, _, e = r.Git("merge-base", "--is-ancestor", mainSHA, sha)
	switch rc {
	case 0:
		return Stale{OK: true, MainSHA: mainSHA}, nil
	case 1:
		return Stale{Why: "STALE", MainSHA: mainSHA}, nil
	}
	return Stale{Why: "git error checking ancestry: " + orRC(e, rc)}, nil
}

// VerifiedMain is verified_main: the verified fetch of origin/main (and the PR head when head != ""). mainSHA is ""
// with why set when anything could not be verified.
func VerifiedMain(r Runner, prNumber int64, repoURL, ownerRepo, head string) (mainSHA, why string, err error) {
	type ref struct{ ref, src, what string }
	refs := []ref{{"refs/factory-judge/main", "refs/heads/main", "origin main"}}
	if head != "" {
		refs = append(refs, ref{fmt.Sprintf("refs/factory-judge/pr-%d", prNumber), fmt.Sprintf("refs/pull/%d/head", prNumber), "PR head"})
	}
	for _, f := range refs {
		rc, _, e := r.Git("-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head", repoURL, "+"+f.src+":"+f.ref)
		if rc != 0 {
			return "", fmt.Sprintf("git fetch %s failed: %s", f.what, orRC(e, rc)), nil
		}
	}
	rc, mainSHA, e := r.Git("rev-parse", "--verify", "refs/factory-judge/main")
	if rc != 0 {
		return "", fmt.Sprintf("origin/main unknown: %s", orRC(e, rc)), nil
	}
	if head != "" {
		rc, prSHA, e := r.Git("rev-parse", "--verify", fmt.Sprintf("refs/factory-judge/pr-%d", prNumber))
		if rc != 0 || prSHA != head {
			return "", fmt.Sprintf("fetched PR head %s is not %s: %s", shortOrQ(prSHA), short(head), orRC(e, rc)), nil
		}
	}
	out, errOut, grc, gerr := r.MainSHA(ownerRepo, GHAPITimeout)
	if gerr != nil {
		return "", "gh api commits/main failed: " + gerr.Error(), nil
	}
	if got := pyStrip(out); grc != 0 || got != mainSHA {
		shown := short(got)
		if shown == "" {
			shown = pyStrip(errOut)
		}
		if shown == "" {
			shown = fmt.Sprint(grc)
		}
		return "", fmt.Sprintf("fetched main %s does not match GitHub's (%s); is the git remote tampered with?", short(mainSHA), shown), nil
	}
	return mainSHA, "", nil
}

// githubSquashCommitter is _GITHUB_SQUASH_COMMITTER.
const githubSquashCommitter = "noreply@github.com"

// SquashMatchOnMain is squash_match_on_main: a GitHub squash of `head` on main (single parent P, GitHub committer,
// subject ending (#N), tree == merge-tree --write-tree P head). "" when none.
func SquashMatchOnMain(r Runner, mainSHA string, prNumber int64, head string, maxCommits int) string {
	rc, out, _ := r.Git("log", fmt.Sprintf("-n%d", maxCommits), "--format=%H %P %T %ce %s", mainSHA)
	if rc != 0 || pyStrip(out) == "" {
		return ""
	}
	suffix := fmt.Sprintf("(#%d)", prNumber)
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, " ", 5)
		for len(f) < 5 {
			f = append(f, "")
		}
		sha, parents, tree, ce, subject := f[0], f[1], f[2], f[3], f[4]
		ps := strings.Fields(parents)
		if len(ps) != 1 {
			continue
		}
		if pyLower(pyStrip(ce)) != githubSquashCommitter {
			continue
		}
		if !strings.HasSuffix(pyStrip(subject), suffix) {
			continue
		}
		rc, mt, _ := r.Git("merge-tree", "--write-tree", ps[0], head)
		if rc != 0 || pyStrip(mt) != pyStrip(tree) {
			continue
		}
		return pyStrip(sha)
	}
	return ""
}

// MainLanding is main_landing: the main commit that already holds the change of PR head `head` (its squash, or the
// head itself when it is an ancestor of main), "" when none or nothing could be verified.
func MainLanding(r Runner, prNumber int64, repoURL, ownerRepo, head string) (string, error) {
	mainSHA, _, err := VerifiedMain(r, prNumber, repoURL, ownerRepo, head)
	if err != nil || mainSHA == "" {
		return "", err
	}
	if landed := SquashMatchOnMain(r, mainSHA, prNumber, head, LandingMaxCommits); landed != "" {
		return landed, nil
	}
	if rc, _, _ := r.Git("merge-base", "--is-ancestor", head, mainSHA); rc == 0 {
		return head, nil
	}
	return "", nil
}
