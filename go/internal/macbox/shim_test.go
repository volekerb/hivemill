package macbox

import (
	"strings"
	"testing"
)

func TestShimNoTokenArgv(t *testing.T) {
	const oauth = "oauth-token-placeholder-7f3a"
	const gh = "gh-token-placeholder-abc"
	o := ShimOpts{
		Account: AccountRW, LauncherPath: DefaultLauncher,
		ProfilePath: "/Users/x/.local/state/factory/workers/a/profile.sb",
		RealClaude:  "/Applications/Claude.app/claude",
		OwnerUID:    501,
		AgentHome:   "/Users/x/.local/state/factory/workers/a/claude",
		ConfigDir:   "/Users/x/.local/state/factory/workers/a/claude",
		GitConfig:   "/Users/x/.local/state/factory/workers/a/gitconfig",
		TmpDir:      "/Users/x/.local/state/factory/workers/a/tmp",
		OAuthToken:  oauth,
		GhToken:     gh,
		CallerEnv:   map[string]string{"PATH": "/bin", "FACTORY_AGENT": "a"},
	}
	script, err := RenderShim(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, oauth) || !strings.Contains(script, gh) {
		t.Fatal("tokens must appear in export lines")
	}
	argv := ShimRecordedArgv(o, []string{"--version"})
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, oauth) || strings.Contains(joined, gh) {
		t.Fatalf("argv must not contain tokens: %s", joined)
	}
	env := ShimEnvMap(o)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != oauth || env["GH_TOKEN"] != gh {
		t.Fatalf("tokens only in env map: %v", env)
	}
}
