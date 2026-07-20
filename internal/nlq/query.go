package nlq

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/search"
)

const (
	SynthesiseMarker = "[SYNTHESISE]"
	DefaultMaxRows   = 100
)

// QueryResult holds the result of an NLQ query.
type QueryResult struct {
	Question string
	SQL      string
	Result   *search.Result
	Answer   string // natural-language answer from synthesis
	Mode     string // "table" or "synthesise"
	Error    string
}

// loadPrompt reads the system prompt and injects today's date and archive range.
func loadPrompt(db *sql.DB) string {
	promptPaths := []string{
		"prompts/nl_to_sql.md",
	}

	// Check ~/.slack-search/prompts/
	if home, err := os.UserHomeDir(); err == nil {
		promptPaths = append(promptPaths, filepath.Join(home, ".slack-search", "prompts", "nl_to_sql.md"))
	}

	// Also check relative to the executable
	if exe, err := os.Executable(); err == nil {
		promptPaths = append(promptPaths, filepath.Join(filepath.Dir(exe), "prompts", "nl_to_sql.md"))
	}

	var promptText string
	for _, p := range promptPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			promptText = string(data)
			break
		}
	}
	if promptText == "" {
		promptText = "You are a SQL expert for a Slack message archive in SQLite.\n\n"
	}

	today := time.Now().Format("Monday, 2006-01-02")
	archiveRange := ""
	if db != nil {
		var oldest, newest sql.NullString
		row := db.QueryRow(
			"SELECT date(min(timestamp), 'unixepoch'), date(max(timestamp), 'unixepoch') FROM messages",
		)
		if row.Scan(&oldest, &newest) == nil && oldest.Valid {
			archiveRange = fmt.Sprintf("Archive date range: %s to %s. ", oldest.String, newest.String)
		}
	}

	header := fmt.Sprintf("Today is %s. The current year is %d. %sWhen the user mentions a date without a year, assume the current year (%d) unless the context clearly refers to a past year. Always use timestamp >= unixepoch('YYYY-MM-DD') for date filtering, never datetime(...) >= 'YYYY-...'.\n\n",
		today, time.Now().Year(), archiveRange, time.Now().Year())
	return header + promptText
}

func loadSynthesisPrompt() string {
	paths := []string{
		"prompts/synthesis.md",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".slack-search", "prompts", "synthesis.md"))
	}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "prompts", "synthesis.md"))
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			today := time.Now().Format("Monday, 2006-01-02")
			return strings.ReplaceAll(string(data), "{today}", today)
		}
	}
	return "You are a helpful assistant analysing Slack archive query results. Answer concisely based on the SQL results provided."
}

var sqlCodeBlockRe = regexp.MustCompile("(?si)```sql\\s*(.*?)```")
var selectRe = regexp.MustCompile(`(?si)(SELECT\s.+?)(?:;|$)`)

func extractSQL(text string) string {
	if m := sqlCodeBlockRe.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	if m := selectRe.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// RunQuery executes the NL→SQL→execute→synthesise pipeline.
func RunQuery(db *sql.DB, question, baseURL, apiKey, model string, maxRows int) (*QueryResult, error) {
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}

	systemPrompt := loadPrompt(db)

	// Phase 1: NL → SQL
	llmResp, err := ChatComplete(baseURL, apiKey, model, systemPrompt, question)
	if err != nil {
		return &QueryResult{Question: question, Error: err.Error()}, err
	}

	synthesise := strings.Contains(llmResp, SynthesiseMarker)
	sqlText := extractSQL(strings.ReplaceAll(llmResp, SynthesiseMarker, ""))

	if sqlText == "" {
		return &QueryResult{
			Question: question,
			Answer:   llmResp,
			Mode:     "answer",
		}, nil
	}

	// Cap rows
	cappedSQL := fmt.Sprintf("SELECT * FROM (%s) _q LIMIT %d", strings.TrimSuffix(strings.TrimSpace(sqlText), ";"), maxRows)

	result, err := search.RunSQL(db, cappedSQL)
	if err != nil {
		return &QueryResult{
			Question: question,
			SQL:      sqlText,
			Error:    fmt.Sprintf("SQL error: %v", err),
			Mode:     "table",
		}, nil
	}

	qr := &QueryResult{
		Question: question,
		SQL:      sqlText,
		Result:   result,
		Mode:     "table",
	}

	// Phase 2: Synthesis
	if synthesise && result != nil && len(result.Rows) > 0 {
		qr.Mode = "synthesise"
		table := resultToMarkdown(result)
		synthPrompt := loadSynthesisPrompt()
		synthQuestion := fmt.Sprintf("Original question: %s\n\nSQL query:\n```sql\n%s\n```\n\nResults (%d rows):\n%s",
			question, sqlText, len(result.Rows), table)

		answer, err := ChatComplete(baseURL, apiKey, model, synthPrompt, synthQuestion)
		if err != nil {
			qr.Error = fmt.Sprintf("synthesis error: %v", err)
		} else {
			qr.Answer = answer
		}
	}

	return qr, nil
}

func resultToMarkdown(r *search.Result) string {
	if r == nil || len(r.Rows) == 0 {
		return "(no results)"
	}
	var b strings.Builder
	b.WriteString("| " + strings.Join(r.Columns, " | ") + " |\n")
	b.WriteString("| " + strings.Repeat("--- | ", len(r.Columns)) + "\n")
	for _, row := range r.Rows {
		vals := make([]string, len(row))
		for i, v := range row {
			vals[i] = fmt.Sprintf("%v", v)
		}
		b.WriteString("| " + strings.Join(vals, " | ") + " |\n")
	}
	return b.String()
}

// ChatComplete calls an OpenAI-compatible chat completion API.
func ChatComplete(baseURL, apiKey, model, systemPrompt, userMessage string) (string, error) {
	body := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
		"temperature": 0.1,
	}
	jsonBody, _ := json.Marshal(body)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read LLM response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse LLM response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}
