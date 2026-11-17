package macbox

import "path"

// ProbeKind is what a probe does through the launch chain.
type ProbeKind string

// Probe kinds.
const (
	ProbeRead  ProbeKind = "read"
	ProbeWrite ProbeKind = "write"
)

// Probe is one self-test step the spawn preflight runs through the full chain.
// SetupAsOwner means the preflight creates the file as the owner first (a
// canary); the probe then must be denied for the sandbox to count as working.
type Probe struct {
	Name         string
	Kind         ProbeKind
	Path         string
	WantAllowed  bool
	SetupAsOwner bool
}

// SelfTestPlan lists the probes for the profile built from in. Pure data.
// nonce is unique per preflight run: every probe path carries it, so two
// concurrent preflights for the same owner never share (or delete) a canary.
func SelfTestPlan(in ProfileInput, nonce string) []Probe {
	sfx := "-" + nonce
	plan := []Probe{
		{Name: "owner-home-canary", Kind: ProbeRead, Path: path.Join(in.OwnerHome, ".hivemill-selftest-canary"+sfx), SetupAsOwner: true},
		{Name: "owner-keychains-denied", Kind: ProbeRead, Path: path.Join(in.OwnerHome, "Library/Keychains/.hivemill-selftest-canary"+sfx), SetupAsOwner: true},
		{Name: "outside-allow-list-unreadable", Kind: ProbeRead, Path: path.Join(in.OwnerHome, ".hivemill-selftest-outside"+sfx), SetupAsOwner: true},
	}
	clone := Probe{Name: "clone-writable", Kind: ProbeWrite, Path: path.Join(in.CloneDir, ".hivemill-selftest-write"+sfx), WantAllowed: in.CloneWritable}
	if !in.CloneWritable {
		clone.Name = "clone-read-only"
	}
	return append(plan, clone)
}
