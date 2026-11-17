package holdexpiry

import (
	"context"
	"database/sql"
)

// The parsers, for the fuzz targets in fuzz_test.go.

func ParseExpiry(v string) int { return parseExpiry(v) }

func PRNumber(url string) int { return prNumber(url) }

func Approval(ctx context.Context, db *sql.DB, taskID, reason string) (valid bool, invalid string, err error) {
	return approval(ctx, db, task{id: taskID, reason: reason})
}
