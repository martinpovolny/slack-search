package slack

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/martinpovolny/slack-search/internal/db"
)

// SearchResult holds a single search result for display.
type SearchResult struct {
	Time      string `json:"time"`
	Channel   string `json:"channel"`
	ChannelID string `json:"channel_id"`
	Author    string `json:"author"`
	Text      string `json:"text"`
	Permalink string `json:"permalink"`
	TS        string `json:"ts"`
}

// LiveSearch queries Slack's search API and caches results locally.
func LiveSearch(conn *sql.DB, client *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 50
	}

	pageSize := limit
	if pageSize > 100 {
		pageSize = 100
	}

	data, err := client.SearchMessages(query, pageSize, 1)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Messages struct {
			Matches []struct {
				TS        string `json:"ts"`
				Text      string `json:"text"`
				Username  string `json:"username"`
				Permalink string `json:"permalink"`
				Channel   struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
			} `json:"matches"`
			Total int `json:"total"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}

	var results []SearchResult
	newCount := 0

	for _, m := range resp.Messages.Matches {
		if len(results) >= limit {
			break
		}

		// Cache in local DB — resolve DM channel names
		channelName := m.Channel.Name
		if strings.HasPrefix(m.Channel.ID, "D") && (strings.HasPrefix(channelName, "U") || channelName == m.Channel.ID) {
			if strings.HasPrefix(channelName, "U") {
				var realName string
				_ = conn.QueryRow("SELECT real_name FROM users WHERE id=?", channelName).Scan(&realName)
				if realName != "" {
					channelName = "DM: " + realName
				}
			}
		}
		_ = db.UpsertChannel(conn, m.Channel.ID, channelName)

		tsFloat, _ := strconv.ParseFloat(m.TS, 64)
		rawJSON, _ := json.Marshal(m)

		inserted, _ := db.InsertMessage(conn, db.Message{
			TS:        m.TS,
			ChannelID: m.Channel.ID,
			Username:  m.Username,
			Text:      m.Text,
			Timestamp: tsFloat,
			RawJSON:   rawJSON,
		})
		if inserted {
			newCount++
		}

		// Format time
		ts, _ := strconv.ParseFloat(m.TS, 64)
		timeStr := ""
		if ts > 0 {
			timeStr = strings.Split(m.TS, ".")[0]
		}

		results = append(results, SearchResult{
			Time:      timeStr,
			Channel:   m.Channel.Name,
			ChannelID: m.Channel.ID,
			Author:    m.Username,
			Text:      m.Text,
			Permalink: m.Permalink,
			TS:        m.TS,
		})
	}

	if newCount > 0 {
		fmt.Printf("%d new message(s) cached in local DB\n", newCount)
	}

	return results, nil
}

// ExtractHighlightTerm extracts the main search term from a Slack query (strips operators).
func ExtractHighlightTerm(query string) string {
	// Prefer quoted phrase
	if idx := strings.Index(query, `"`); idx >= 0 {
		if end := strings.Index(query[idx+1:], `"`); end >= 0 {
			return query[idx+1 : idx+1+end]
		}
	}

	// Strip operators
	words := strings.Fields(query)
	var clean []string
	for _, w := range words {
		if strings.HasPrefix(w, "in:") || strings.HasPrefix(w, "from:") ||
			strings.HasPrefix(w, "before:") || strings.HasPrefix(w, "after:") ||
			strings.HasPrefix(w, "during:") || strings.HasPrefix(w, "to:") ||
			strings.HasPrefix(w, "has:") || strings.HasPrefix(w, "is:") ||
			strings.HasPrefix(w, "-") {
			continue
		}
		clean = append(clean, w)
	}
	return strings.Join(clean, " ")
}
