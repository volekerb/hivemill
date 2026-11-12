package guard

import "testing"

func TestHcParity(t *testing.T) {
	clearTermsEnv(t)
	for _, cmd := range []string{
		"hc server stop",
		"herdr-core server stop",
		"HOME=/tmp hc server stop",
		"env -i hc server stop",
		"bash -c 'hc server stop'",
		"hc kill other-agent",
		"HC_SOCKET=/tmp/x hc ls",
		"export HC_SOCKET=/tmp/x",
		"unset HC_SOCKET; hc ls",
		"HERDR_SOCKET_PATH=/tmp/core.sock hc ls",
	} {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("worker %q: allowed", cmd)
		} else if IsDelegate(err) {
			t.Errorf("worker %q: delegate %v", cmd, err)
		}
	}
	for _, cmd := range []string{
		"echo hc",
		"grep hc README.md",
		"echo HC_SOCKET=/tmp/x",
		"git status",
	} {
		if err := Check(cmd, "", "coder", false); err != nil && !IsDelegate(err) {
			t.Errorf("worker allow %q: %v", cmd, err)
		}
	}
	for _, cmd := range []string{
		"hc server stop",
		"herdr-core server stop",
		"hc kill other-agent",
		"herdr server stop",
	} {
		if err := Check(cmd, "", "", true); err != nil {
			t.Errorf("hq %q: %v", cmd, err)
		}
	}
	for _, cmd := range []string{"sudo hc server stop", "sudo herdr server stop"} {
		if err := Check(cmd, "", "", true); !IsPrivBlock(err) {
			t.Errorf("hq priv %q: %v", cmd, err)
		}
	}
}
