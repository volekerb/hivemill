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
func SelfTestPlan(in ProfileInput) []Probe {
	plan := []Probe{
		{Name: "owner-home-canary", Kind: ProbeRead, Path: path.Join(in.OwnerHome, ".hivemill-selftest-canary"), SetupAsOwner: true},
		{Name: "owner-keychains-denied", Kind: ProbeRead, Path: path.Join(in.OwnerHome, "Library/Keychains/.hivemill-selftest-canary"), SetupAsOwner: true},
		{Name: "outside-allow-list-unreadable", Kind: ProbeRead, Path: path.Join(in.OwnerHome, ".hivemill-selftest-outside"), SetupAsOwner: true},
	}
	clone := Probe{Name: "clone-writable", Kind: ProbeWrite, Path: path.Join(in.CloneDir, ".hivemill-selftest-write"), WantAllowed: in.CloneWritable}
	if !in.CloneWritable {
		clone.Name = "clone-read-only"
	}
	return append(plan, clone)
}
