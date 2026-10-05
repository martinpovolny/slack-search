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
	memberNamesCache := make(map[string][]string)
	userNamesCache := make(map[string]string)

	userDisplayName := func(userID string) string {
		if userID == "" {
			return ""
		}
		if name, ok := userNamesCache[userID]; ok {
			return name
		}
		var realName, displayName, username string
		_ = conn.QueryRow("SELECT real_name, display_name, name FROM users WHERE id=?", userID).Scan(&realName, &displayName, &username)
		name := displayName
		if name == "" {
			name = realName
		}
		if name == "" {
			name = username
		}
		if name == "" {
			data, err := client.UsersInfo(userID)
			if err == nil {
				var resp struct {
					User struct {
						ID      string `json:"id"`
						Name    string `json:"name"`
						Profile struct {
							RealName    string `json:"real_name"`
							DisplayName string `json:"display_name"`
						} `json:"profile"`
					} `json:"user"`
				}
				if json.Unmarshal(data, &resp) == nil {
					name = resp.User.Profile.DisplayName
					if name == "" {
						name = resp.User.Profile.RealName
					}
					if name == "" {
						name = resp.User.Name
					}
					if resp.User.ID != "" {
						_ = db.UpsertUser(conn, resp.User.ID, resp.User.Name, resp.User.Profile.RealName, resp.User.Profile.DisplayName)
					}
				}
			}
		}
		if name == "" {
			name = userID
		}
		userNamesCache[userID] = name
		return name
	}

	conversationMembers := func(channelID string) []string {
		if names, ok := memberNamesCache[channelID]; ok {
			return names
		}
		var names []string
		data, err := client.ConversationsInfo(channelID)
		if err == nil {
			var resp struct {
				Channel struct {
					Members []string `json:"members"`
					User    string   `json:"user"`
				} `json:"channel"`
			}
			if json.Unmarshal(data, &resp) == nil {
				members := resp.Channel.Members
				if len(members) == 0 && resp.Channel.User != "" {
					members = []string{resp.Channel.User}
				}
				for _, memberID := range members {
					if name := userDisplayName(memberID); name != "" {
						names = append(names, name)
					}
				}
			}
		}
		memberNamesCache[channelID] = names
		return names
	}

	for _, m := range resp.Messages.Matches {
		if len(results) >= limit {
			break
		}

		// Cache in local DB — resolve DM and multi-person DM names.
		channelName := m.Channel.Name
		var memberNames []string
		if strings.HasPrefix(m.Channel.ID, "G") {
			memberNames = conversationMembers(m.Channel.ID)
		} else if strings.HasPrefix(m.Channel.ID, "D") && strings.HasPrefix(channelName, "U") {
			memberNames = []string{userDisplayName(channelName)}
		} else if strings.HasPrefix(m.Channel.ID, "D") && (channelName == "" || channelName == m.Channel.ID) {
			memberNames = conversationMembers(m.Channel.ID)
		}
		if len(memberNames) > 0 {
			channelName = "DM: " + strings.Join(memberNames, ", ")
		}
		_ = db.UpsertChannel(conn, m.Channel.ID, channelName)
		if len(memberNames) > 0 {
			_ = db.UpsertChannelMembers(conn, m.Channel.ID, memberNames)
		}

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
