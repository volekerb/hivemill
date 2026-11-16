package macbox

import "fmt"

// Spawn roles that use macbox (gate uses a stricter profile at test time; spawn
// wiring is M6b).
var sandboxSpawnRoles = map[string]bool{
	"reviewer": true, "qa": true, "coder": true, "researcher": true,
}

// AccountForRole maps a factory worker role to the hidden macOS account.
func AccountForRole(role string) (string, error) {
	if !sandboxSpawnRoles[role] {
		return "", fmt.Errorf("macbox: role %q is not sandboxed on macOS", role)
	}
	switch role {
	case "coder":
		return AccountRW, nil
	case "reviewer", "qa", "researcher":
		return AccountRO, nil
	default:
		return "", fmt.Errorf("macbox: unknown role %q", role)
	}
}
