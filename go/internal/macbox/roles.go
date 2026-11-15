package macbox

import "fmt"

// RolePaths are the per-agent paths ForRole maps onto a ProfileInput.
type RolePaths struct {
	OwnerHome     string
	FactoryHome   string
	AgentDir      string
	CoderClone    string // the task's coder clone
	QAClone       string // qa's own second clone of the task
	GateTree      string // the tree the HQ gate runs in
	ReadOnlyPaths []string
}

// ForRole builds the ProfileInput of ADR 0003 section 3.2:
// coder -> its clone RW; qa -> its own qa clone RW; reviewer -> the coder
// clone RO; gate -> tree RW, no network, no tokens.
func ForRole(role Role, p RolePaths) (ProfileInput, error) {
	in := ProfileInput{
		Role:          role,
		OwnerHome:     p.OwnerHome,
		FactoryHome:   p.FactoryHome,
		AgentDir:      p.AgentDir,
		ReadOnlyPaths: p.ReadOnlyPaths,
		Network:       true,
	}
	switch role {
	case RoleCoder:
		in.CloneDir, in.CloneWritable = p.CoderClone, true
	case RoleQA:
		in.CloneDir, in.CloneWritable = p.QAClone, true
	case RoleReviewer:
		in.CloneDir, in.CloneWritable = p.CoderClone, false
	case RoleGate:
		in.CloneDir, in.CloneWritable, in.Network = p.GateTree, true, false
	default:
		return ProfileInput{}, fmt.Errorf("macbox: unknown role %q (want reviewer, qa, coder or gate)", role)
	}
	if in.CloneDir == "" {
		return ProfileInput{}, fmt.Errorf("macbox: role %s needs its clone path set", role)
	}
	return in, nil
}
