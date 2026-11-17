package views

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"factory/go/internal/web/data"
)

// goalFilter is the _goal_filter of the throughput, quality and changelog pages: the goals of the ?org= filter, nil
// (no filter) without one.
func goalFilter(ctx *Ctx) (data.GoalSet, string, error) {
	org := ctx.org()
	if org == "" {
		return nil, "", nil
	}
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return nil, "", fmt.Errorf("views: goal orgs: %w", err)
	}
	rows, err := ctx.DB.Query("SELECT id FROM goals")
	if err != nil {
		return nil, "", fmt.Errorf("views: read goals: %w", err)
	}
	defer rows.Close()
	goals := data.GoalSet{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, "", fmt.Errorf("views: scan goals: %w", err)
		}
		if data.OrgOfGoal(gorgs, id) == org {
			goals[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("views: read goals: %w", err)
	}
	return goals, org, nil
}

// roundN is Python's round(x, n): the correctly rounded decimal (ties to even on the exact binary value).
func roundN(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if err != nil {
		return x
	}
	return r
}

// pySumFloats is Python's sum() over floats: CPython 3.12+ uses Neumaier compensated summation.
func pySumFloats(xs []float64) float64 {
	f, c := 0.0, 0.0
	for _, x := range xs {
		t := f + x
		if math.Abs(f) >= math.Abs(x) {
			c += (f - t) + x
		} else {
			c += (x - t) + f
		}
		f = t
	}
	return f + c
}

// pyMedianFloats is statistics.median of numbers: the middle one, or the mean of the two middle ones.
func pyMedianFloats(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// medianRounded is round(statistics.median(xs)) of integers, or false when xs is empty.
func medianRounded(xs []int64) (int64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return pyRound(float64(s[n/2-1]+s[n/2]) / 2), true
}

// p90Int is the nearest-rank 90th percentile of integers, or false when xs is empty.
func p90Int(xs []int64) (int64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[max(0, int(math.Ceil(0.9*float64(len(s))))-1)], true
}

// p90Float is the nearest-rank 90th percentile of floats.
func p90Float(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[max(0, int(math.Ceil(0.9*float64(len(s))))-1)]
}

// localMonday is the ISO date of the Monday of the local week of t.
func localMonday(t time.Time) string {
	l := t.In(data.Loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.UTC)
	wd := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -wd).Format("2006-01-02")
}
