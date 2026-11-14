package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

var allDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ValidationError is a refused add with exit code 2 (nothing written).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ValidateHour returns an hour in 0..23.
func ValidateHour(h int) error {
	if h < 0 || h > 23 {
		return &ValidationError{Msg: fmt.Sprintf("hour must be 0–23, not %d", h)}
	}
	return nil
}

// ValidateDays checks days is a non-empty subset of mon..sun.
func ValidateDays(days string) (string, error) {
	if days == "" {
		return strings.Join(allDays, ","), nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(days, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		ok := false
		for _, d := range allDays {
			if p == d {
				ok = true
				break
			}
		}
		if !ok {
			return "", &ValidationError{Msg: fmt.Sprintf("unknown weekday %q in --days", p)}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "", &ValidationError{Msg: "--days must name at least one weekday"}
	}
	return strings.Join(out, ","), nil
}

// ValidateJob checks job text rules for add.
func ValidateJob(job string) error {
	if job == "" {
		return &ValidationError{Msg: "job text must not be empty"}
	}
	if len(job) > 500 {
		return &ValidationError{Msg: fmt.Sprintf("job text is %d chars; max is 500", len(job))}
	}
	if strings.ContainsAny(job, "\n\r") {
		return &ValidationError{Msg: "job text must be a single line"}
	}
	for _, r := range job {
		if unicode.IsControl(r) {
			return &ValidationError{Msg: "job text must not contain control characters"}
		}
	}
	if strings.Contains(job, "<<<") || strings.Contains(job, ">>>") {
		return &ValidationError{Msg: `job text must not contain "<<<" or ">>>"`}
	}
	return nil
}

// ParseHour parses --hour.
func ParseHour(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, &ValidationError{Msg: "hour must be an integer 0–23"}
	}
	return n, ValidateHour(n)
}

// ParseTokenCap parses --token-cap.
func ParseTokenCap(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, &ValidationError{Msg: "token-cap must be a positive integer"}
	}
	return n, nil
}
