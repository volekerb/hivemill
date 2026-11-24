package cli

import "factory/go/internal/digest"

// The scoreboard helpers (scorecard_rows, lead_counts, director_row, digest_clock, _ts) live in go/internal/digest, which the
// tick also uses; these are the names the cli commands and tests call them by.
type (
	LeadCounts  = digest.LeadCounts
	ScoreRow    = digest.ScoreRow
	DirectorRow = digest.DirectorRow
)

var (
	sbParseTS      = digest.ParseTS
	sbLocation     = digest.Location
	sbClock        = digest.Clock
	sbISO          = digest.ISO
	sbCount        = digest.Count
	sbLeadCounts   = digest.CountLeads
	sbScoreRows    = digest.ScoreRows
	sbDirectorRow  = digest.DirectorRowAt
	sbDirectorLine = digest.DirectorLine
	pyIntStr       = digest.PyIntStr
	sbOwnerCapKeys = *digest.OwnerCapKeys
	sbWorkerRoles  = *digest.WorkerRoles
)
