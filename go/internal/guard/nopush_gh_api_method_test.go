package guard

import (
	"strings"
	"testing"
)

// T-0987 / T-1030 (tests/test_worker_guard_nopush_gh_api_method.py): non-GET gh api methods and method-override headers.

var (
	ghAPIMethodDenied = []string{
		"gh api -X DELETE repos/acme/x/issues/1",
		"gh api -X PUT repos/acme/x/issues/1/lock",
		"gh api -X POST repos/acme/x/issues/1/comments",
		"gh api -X PATCH repos/acme/x/issues/1",
		"gh api --method DELETE repos/acme/x/issues/1",
		"gh api --method PUT repos/acme/x/issues/1/lock",
		"gh api --method POST repos/acme/x/issues/1/comments",
		"gh api --method=DELETE repos/acme/x/issues/1",
		"gh api -XDELETE repos/acme/x/issues/1",
		"gh api repos/acme/x/issues/1 -X delete",
		"gh -R acme/x api -X POST repos/acme/x/issues",
		"gh api -X HEAD repos/acme/x",
		"gh api -X OPTIONS repos/acme/x",
		"gh api -X '' repos/acme/x",
		"gh api -X $M repos/acme/x",
		"gh api repos/acme/x -X",
	}
	ghAPIMethodOverrideDenied = []string{
		"gh api -H 'X-HTTP-Method-Override: POST' repos/acme/x/issues",
		"gh api -H 'X-HTTP-Method-Override: PUT' repos/acme/x/issues/1/lock",
		"gh api --header 'x-http-method-override: patch' repos/acme/x/issues/1",
		"gh api repos/acme/x/issues/1 -H 'X-HTTP-Method-Override:DELETE'",
		"gh api -H 'Accept: application/json' -H 'X-HTTP-Method-Override: DELETE' repos/acme/x/labels/y",
		"gh api --header='X-HTTP-Method-Override: PUT' repos/acme/x/issues/1/lock",
		"gh api -X GET -H 'X-HTTP-Method-Override: DELETE' repos/acme/x/labels/y",
	}
	ghAPIMethodAllowed = []string{
		"gh api repos/acme/x",
		"gh api -X GET repos/acme/x",
		"gh api --method GET repos/acme/x",
		"gh api --method=get repos/acme/x",
		"gh api -XGET repos/acme/x -f per_page=5",
		"gh api -H 'Accept: application/vnd.github+json' repos/acme/x",
		"gh api -H 'X-HTTP-Method-Override: GET' repos/acme/x",
	}
)

func TestNoPushGhAPIMethodDenied(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"qa", "reviewer", "pm"} {
		for _, cmd := range append(ghAPIMethodDenied, ghAPIMethodOverrideDenied...) {
			err := Check(cmd, t.TempDir(), role, false)
			if exitOf(err) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
	}
}

func TestNoPushGhAPIMethodHeadOptionsMessage(t *testing.T) {
	clearTermsEnv(t)
	for _, cmd := range []string{"gh api -X HEAD repos/acme/x", "gh api -X OPTIONS repos/acme/x"} {
		err := Check(cmd, t.TempDir(), "qa", false)
		if err == nil || !strings.Contains(err.Error(), "is not GET") || strings.Contains(err.Error(), "is a write") {
			t.Errorf("%q: %v", cmd, err)
		}
	}
}

func TestNoPushGhAPIMethodAllowed(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"qa", "reviewer", "pm"} {
		for _, cmd := range ghAPIMethodAllowed {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q refused: %v", role, cmd, err)
			}
		}
	}
}

func TestNoPushGhAPIMethodOtherRolesUnchanged(t *testing.T) {
	clearTermsEnv(t)
	all := append(append(ghAPIMethodDenied, ghAPIMethodOverrideDenied...), ghAPIMethodAllowed...)
	for _, role := range []string{"coder", "judge"} {
		for _, cmd := range all {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q: %v", role, cmd, err)
			}
		}
	}
}
