package s2restier

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Port of tests/test_res_hq_cap.py (2 cases; see ported_s2cli_restier.txt).

var hqCapInWorkersRE = regexp.MustCompile(`· hq (\p{Nd}+)/(\p{Nd}+) \(reserved for workers 10\)`)

func workersLiveLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "workers live ") {
			return l
		}
	}
	return ""
}

func TestResHqCapResAndGateAgreeOnSmallBox(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	e.OK("config", "max_live_agents", "6")
	e.OK("config", "reserved_worker_slots", "10")
	gateCap := gateHQCap(t, e)
	if gateCap != 3 {
		t.Fatalf("gate hq_cap = %d, want 3", gateCap)
	}
	line := workersLiveLine(e.OK("res").Stdout)
	m := hqCapInWorkersRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no hq cap in %q", line)
	}
	resCap, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatal(err)
	}
	if resCap != gateCap {
		t.Fatalf("res cap %d != gate cap %d", resCap, gateCap)
	}
}

func TestResHqCapResAndGateAgreeOnBigBox(t *testing.T) {
	t.Parallel()
	e := capBaseEnv(t)
	gateCap := gateHQCap(t, e)
	if gateCap != 8 {
		t.Fatalf("gate hq_cap = %d, want 8", gateCap)
	}
	line := workersLiveLine(e.OK("res").Stdout)
	if !strings.Contains(line, "· hq 0/8 (reserved for workers 10)") {
		t.Fatalf("workers line = %q", line)
	}
}
