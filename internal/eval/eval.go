package eval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/nlq"
)

// Test represents a single NLQ evaluation test case.
type Test struct {
	ID           string     `json:"id"`
	Question     string     `json:"question"`
	ExpectedMode string     `json:"expected_mode"` // "table" or "synthesise"
	SQLChecks    []SQLCheck `json:"sql_checks"`
}

// SQLCheck is a single SQL validation rule.
type SQLCheck struct {
	Type  string `json:"type"`  // "contains" or "not_contains"
	Value string `json:"value"` // substring to check
	Desc  string `json:"desc"`  // description
}

// TestResult holds the outcome of running one test.
type TestResult struct {
	ID       string       `json:"id"`
	Question string       `json:"question"`
	SQL      string       `json:"sql"`
	Mode     string       `json:"mode"`
	Checks   []CheckResult `json:"checks"`
	Passed   bool         `json:"passed"`
	Error    string       `json:"error,omitempty"`
}

// CheckResult is one check's pass/fail status.
type CheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// LoadTests reads test cases from YAML-like JSON files in a directory.
func LoadTests(dir string) ([]Test, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read test dir %s: %w", dir, err)
	}

	var tests []Test
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t Test
		if json.Unmarshal(data, &t) == nil && t.ID != "" {
			tests = append(tests, t)
		}
	}
	return tests, nil
}

// RunEval executes all test cases against the NLQ pipeline.
func RunEval(db *sql.DB, tests []Test, baseURL, apiKey, model string) []TestResult {
	var results []TestResult

	for _, t := range tests {
		fmt.Printf("  [%s] %s\n", t.ID, t.Question)

		qr, err := nlq.RunQuery(db, t.Question, baseURL, apiKey, model, 100)

		tr := TestResult{
			ID:       t.ID,
			Question: t.Question,
			Passed:   true,
		}

		if err != nil || qr == nil {
			tr.Error = fmt.Sprintf("%v", err)
			tr.Passed = false
			results = append(results, tr)
			continue
		}

		tr.SQL = qr.SQL
		tr.Mode = qr.Mode

		// Check mode
		if t.ExpectedMode != "" && t.ExpectedMode != qr.Mode {
			tr.Checks = append(tr.Checks, CheckResult{
				Name:   "mode",
				Passed: false,
				Detail: fmt.Sprintf("expected %s, got %s", t.ExpectedMode, qr.Mode),
			})
			tr.Passed = false
		}

		// SQL checks
		sqlLower := strings.ToLower(qr.SQL)
		for _, sc := range t.SQLChecks {
			valueLower := strings.ToLower(sc.Value)
			cr := CheckResult{Name: sc.Desc}
			switch sc.Type {
			case "contains":
				cr.Passed = strings.Contains(sqlLower, valueLower)
				if !cr.Passed {
					cr.Detail = fmt.Sprintf("SQL should contain '%s'", sc.Value)
				}
			case "not_contains":
				cr.Passed = !strings.Contains(sqlLower, valueLower)
				if !cr.Passed {
					cr.Detail = fmt.Sprintf("SQL should NOT contain '%s'", sc.Value)
				}
			}
			tr.Checks = append(tr.Checks, cr)
			if !cr.Passed {
				tr.Passed = false
			}
		}

		results = append(results, tr)
	}

	return results
}

// SaveResults writes eval results to a JSON file.
func SaveResults(results []TestResult, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create results dir %s: %w", dir, err)
	}
	ts := time.Now().Format("2006-01-02T15-04-05")
	path := filepath.Join(dir, fmt.Sprintf("eval-%s.json", ts))
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// PrintSummary prints a pass/fail summary of eval results.
func PrintSummary(results []TestResult) {
	passed, failed := 0, 0
	for _, r := range results {
		if r.Passed {
			passed++
			fmt.Printf("  ✓ [%s] %s\n", r.ID, r.Question)
		} else {
			failed++
			fmt.Printf("  ✗ [%s] %s\n", r.ID, r.Question)
			for _, c := range r.Checks {
				if !c.Passed {
					fmt.Printf("      %s: %s\n", c.Name, c.Detail)
				}
			}
			if r.Error != "" {
				fmt.Printf("      error: %s\n", r.Error)
			}
		}
	}
	fmt.Printf("\n%d passed, %d failed out of %d tests\n", passed, failed, len(results))
}
