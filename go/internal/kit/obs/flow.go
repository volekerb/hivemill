package obs

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"factory/go/internal/prflow"
)

// FlowWindows are the windows (hours) a pr-flow snapshot is kept for: the time ranges of the web UI (24 h, 72 h,
// 7 d, 30 d) and twice each, so merges in the previous window are the difference of the two.
var FlowWindows = []int{24, 48, 72, 144, 168, 336, 720, 1440}

// RunFlow is pr-flow's run; tests replace it so no gh call is made.
var RunFlow = prflow.Run

// refreshFlow stores a pr-flow report per project repo and window, when the last refresh is older than FlowEvery.
// It never fails the ingest: the result says what happened.
func refreshFlow(db *sql.DB, o IngestOptions) string {
	if o.FlowEvery <= 0 {
		return "skipped"
	}
	if last, err := strconv.ParseInt(metaGet(db, "flow_ms"), 10, 64); err == nil && o.Now.Sub(time.UnixMilli(last)) < o.FlowEvery {
		return "fresh"
	}
	repos, err := projectRepos(o.Paths.FactoryDB)
	if err != nil {
		return "error: " + err.Error()
	}
	events, err := os.ReadFile(o.Paths.Events)
	if err != nil && !os.IsNotExist(err) {
		return "error: read events: " + err.Error()
	}
	var errs []string
	for _, pr := range repos {
		for _, h := range FlowWindows {
			rep, err := RunFlow(prflow.Options{Repo: pr.repo, Since: time.Duration(h) * time.Hour, Now: o.Now,
				Events: bytes.NewReader(events)})
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s %dh: %v", pr.repo, h, err))
				continue
			}
			body, err := json.Marshal(rep)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s %dh: %v", pr.repo, h, err))
				continue
			}
			if _, err := db.Exec(`INSERT OR REPLACE INTO flow(repo, project, hours, at_ms, report) VALUES(?,?,?,?,?)`,
				pr.repo, pr.project, h, o.Now.UnixMilli(), string(body)); err != nil {
				return "error: write flow: " + err.Error()
			}
		}
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES('flow_ms', ?)`, o.Now.UnixMilli()); err != nil {
		return "error: write flow time: " + err.Error()
	}
	if len(errs) > 0 {
		return fmt.Sprintf("error: %d of %d snapshots failed, first: %s", len(errs), len(repos)*len(FlowWindows), errs[0])
	}
	return "refreshed"
}

type projectRepo struct{ project, repo string }

// projectRepos lists the factory projects that have a GitHub repo, from factory.db (read-only).
func projectRepos(factoryDB string) ([]projectRepo, error) {
	db, err := openRO(factoryDB, "set --home to the factory home")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, gh_repo FROM projects WHERE COALESCE(gh_repo,'') LIKE '%_/_%' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read projects from factory.db: %w", err)
	}
	defer rows.Close()
	var out []projectRepo
	seen := map[string]bool{}
	for rows.Next() {
		var p projectRepo
		if err := rows.Scan(&p.project, &p.repo); err != nil {
			return nil, fmt.Errorf("read projects: %w", err)
		}
		if !seen[p.repo] {
			seen[p.repo] = true
			out = append(out, p)
		}
	}
	return out, rows.Err()
}
