package guard

import (
	"strings"
	"testing"
	"time"
)

// T-0961: PublicText.push refuses a no-push role before any scan; ptGit has a deadline that the env can lower, not raise.
func TestPtPushNoPushRefusesBeforeScan(t *testing.T) {
	pt := &publicText{cwd: "/nonexistent-dir-for-t0961", pcwdOK: true, nopush: true}
	err := pt.push("/nonexistent-dir-for-t0961", true, "origin", []string{"origin", "HEAD:factory/t-1"})
	if err == nil || !isPublicBlock(err) || !strings.Contains(err.Error(), "may not push") {
		t.Fatalf("no-push role: want a PublicBlock 'may not push', got %v", err)
	}
	pt.nopush = false // a coder reaches the scan code: the old "known only at run time" refusal comes first here
	err = pt.push("/nonexistent-dir-for-t0961", true, "origin", []string{"origin", "HEAD:factory/t-1"})
	if err == nil || strings.Contains(err.Error(), "may not push") {
		t.Fatalf("coder: want the scan path, got %v", err)
	}
}

func TestPtGitTimeoutLowerNotRaise(t *testing.T) {
	for val, want := range map[string]time.Duration{
		"1": time.Second, "0.5": 500 * time.Millisecond, "9999": ptGitTimeoutMax, "0": ptGitTimeoutMax, "x": ptGitTimeoutMax, "": ptGitTimeoutMax,
	} {
		t.Setenv("FACTORY_PT_GIT_TIMEOUT", val)
		if got := ptGitTimeout(); got != want {
			t.Errorf("FACTORY_PT_GIT_TIMEOUT=%q: got %v, want %v", val, got, want)
		}
	}
}
