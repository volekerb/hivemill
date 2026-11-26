package guard

import (
	"io"
	"strings"
)

// The secret checks alone (T-2098): the gate runs them for a repo that is not in public_text_repos. Owner terms and the
// rules below (who and where, not a credential) are a publication concern; every other rule reports a token or key.

// identityRules are the rule ids that are no secret. A genRules rule not listed here counts as a secret, so a new one
// is checked in private repos too. extraRule holds identity rules only and is off as a whole in this mode: a token or
// key rule belongs in genRules.
var identityRules = map[string]bool{
	idOwnerTerm: true, "home-path": true, "rfc1918": true, "cgnat": true, "email": true, "ipv4": true, "private-host": true,
}

// CISecretsGuardMain is CIPublicTextGuardMain with the secret rules only. Owner terms are optional (--terms-file):
// loaded ones mask hit locations in the output and are never a hit.
func CISecretsGuardMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return ciPTGMain(args, stdin, stdout, stderr, true)
}

// CIHitParts says which parts the hit lines of a guard's output ("<rule id> <where> <masked>") belong to: owner is the
// owner terms and identity rules, secrets the token and key rules.
func CIHitParts(output string) (owner, secrets bool) {
	scanOnce.Do(buildRules)
	ids := map[string]bool{idOwnerTerm: true}
	for _, rules := range [][]*scanRule{genRules, extraRule} {
		for _, r := range rules {
			ids[r.id] = true
		}
	}
	for _, ln := range strings.Split(output, "\n") {
		switch id, _, _ := strings.Cut(ln, " "); {
		case !ids[id]:
		case identityRules[id]:
			owner = true
		default:
			secrets = true
		}
	}
	return owner, secrets
}
