package guard

import "testing"

// Mirror of tests/test_worker_guard_hc_parity.py, one to one, same order.
// Counts: worker-block 49, worker-allow 7, HQ-allow 4, HQ-priv 2.

var hcWorkerBlock = []string{
	"hc server stop",
	"herdr-core server stop",
	"HOME=/tmp hc server stop",
	"env -i hc server stop",
	"bash -c 'hc server stop'",
	"hc kill other-agent",
	"hc run --bg --name x claude",
	"hc send other-agent hi",
	"hc keys other-agent Enter",
	"hc rm other-agent",
	"hc read other-agent",
	"hc ls",
	"herdr-core send x hi",
	"herdr-core run --bg --name x claude",
	"HC_SOCKET=/tmp/x hc ls",
	"export HC_SOCKET=/tmp/x",
	"export $(echo HC_SOCKET=/tmp/x)",
	"export `echo HC_SOCKET=/tmp/x`",
	"! export $(echo HC_SOCKET=/tmp/x)",
	"time export $(echo HC_SOCKET=/tmp/x)",
	"if true; then export $(echo HC_SOCKET=/tmp/x); fi",
	"while true; do export $(echo HC_SOCKET=/tmp/x); break; done",
	"! declare -x $(echo HC_SOCKET=/tmp/x)",
	"if true; then declare -x $(echo HC_SOCKET=/tmp/x); fi",
	"! readonly $(echo HC_SOCKET=/tmp/x)",
	"time -p export $(echo HC_SOCKET=/tmp/x)",
	"time -p declare -x $(echo HC_SOCKET=/tmp/x)",
	"time -p typeset -x $(echo HC_SOCKET=/tmp/x)",
	"! time -p export $(echo HC_SOCKET=/tmp/x)",
	"if true; then time -p export $(echo HC_SOCKET=/tmp/x); fi",
	"while true; do time -p export $(echo HC_SOCKET=/tmp/x); break; done",
	"time -p readonly $(echo HC_SOCKET=/tmp/x)",
	"readonly x=$(echo HERDR_SOCKET_PATH=/tmp/x)",
	"\\export $(echo HC_SOCKET=/tmp/x)",
	"\"export\" $(echo HC_SOCKET=/tmp/x)",
	"ex''port $(echo HC_SOCKET=/tmp/x)",
	"e\\xport $(echo HC_SOCKET=/tmp/x)",
	"\\declare -x $(echo HC_SOCKET=/tmp/x)",
	"\\typeset -x $(echo HC_SOCKET=/tmp/x)",
	"\\readonly $(echo HC_SOCKET=/tmp/x)",
	"time -p \\export $(echo HC_SOCKET=/tmp/x)",
	"! \\export $(echo HC_SOCKET=/tmp/x)",
	"if true; then \\export $(echo HC_SOCKET=/tmp/x); fi",
	"while true; do \\export $(echo HC_SOCKET=/tmp/x); break; done",
	"{ \\export $(echo HC_SOCKET=/tmp/x); }",
	"\\export `echo HC_SOCKET=/tmp/x`",
	"eval export `echo HC_SOCKET=/tmp/x`",
	"unset HC_SOCKET; hc ls",
	"HERDR_SOCKET_PATH=/tmp/core.sock hc ls",
}

var hcWorkerAllow = []string{
	"echo hc",
	"grep hc README.md",
	"cat tests/stubs/hc",
	"echo HC_SOCKET=/tmp/x",
	"git status",
	"time -p git status",
	"time git status",
}

var hcHQAllow = []string{
	"herdr server stop",
	"hc server stop",
	"herdr-core server stop",
	"hc kill other-agent",
}

var hcHQPriv = []string{
	"sudo hc server stop",
	"sudo herdr server stop",
}

func TestHcParity(t *testing.T) {
	clearTermsEnv(t)
	t.Run("worker-block", func(t *testing.T) {
		for _, cmd := range hcWorkerBlock {
			if err := Check(cmd, "", "coder", false); err == nil {
				t.Errorf("worker %q: allowed", cmd)
			} else if IsDelegate(err) {
				t.Errorf("worker %q: delegate %v", cmd, err)
			}
		}
	})
	t.Run("worker-allow", func(t *testing.T) {
		for _, cmd := range hcWorkerAllow {
			if err := Check(cmd, "", "coder", false); err != nil && !IsDelegate(err) {
				t.Errorf("worker allow %q: %v", cmd, err)
			}
		}
	})
	t.Run("hq-allow", func(t *testing.T) {
		for _, cmd := range hcHQAllow {
			if err := Check(cmd, "", "", true); err != nil {
				t.Errorf("hq %q: %v", cmd, err)
			}
		}
	})
	t.Run("hq-priv", func(t *testing.T) {
		for _, cmd := range hcHQPriv {
			if err := Check(cmd, "", "", true); !IsPrivBlock(err) {
				t.Errorf("hq priv %q: %v", cmd, err)
			}
		}
	})
}
