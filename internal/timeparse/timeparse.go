package timeparse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var relativeRe = regexp.MustCompile(`(?i)^(\d+)\s+(second|minute|hour|day|week|month)s?\s+ago$`)

// Parse converts a human-readable date string to a Unix timestamp string.
// Supports: "3 weeks ago", "yesterday", "today", ISO dates, and raw timestamps.
func Parse(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}

	// Already a unix timestamp
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s, nil
	}

	now := time.Now()

	// Relative: "N unit ago"
	if m := relativeRe.FindStringSubmatch(s); len(m) == 3 {
		n, _ := strconv.Atoi(m[1])
		unit := strings.ToLower(m[2])
		var d time.Duration
		switch unit {
		case "second":
			d = time.Duration(n) * time.Second
		case "minute":
			d = time.Duration(n) * time.Minute
		case "hour":
			d = time.Duration(n) * time.Hour
		case "day":
			d = time.Duration(n) * 24 * time.Hour
		case "week":
			d = time.Duration(n) * 7 * 24 * time.Hour
		case "month":
			d = time.Duration(n) * 30 * 24 * time.Hour
		}
		return fmt.Sprintf("%.6f", float64(now.Add(-d).Unix())), nil
	}

	// Named
	lower := strings.ToLower(s)
	switch lower {
	case "yesterday":
		t := now.AddDate(0, 0, -1)
		return fmt.Sprintf("%.6f", float64(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Unix())), nil
	case "today":
		t := now
		return fmt.Sprintf("%.6f", float64(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Unix())), nil
	case "last week":
		return fmt.Sprintf("%.6f", float64(now.Add(-7*24*time.Hour).Unix())), nil
	case "last month":
		return fmt.Sprintf("%.6f", float64(now.AddDate(0, -1, 0).Unix())), nil
	}

	// Try common date formats
	formats := []string{
		"2006-01-02",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006/01/02",
		"01/02/2006",
		"Jan 2, 2006",
		"January 2, 2006",
		"2 Jan 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return fmt.Sprintf("%.6f", float64(t.Unix())), nil
		}
	}

	return "", fmt.Errorf("cannot parse date: %q (try '3 days ago', 'yesterday', or '2024-01-01')", s)
}
