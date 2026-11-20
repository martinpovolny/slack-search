package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

type channelScanClient interface {
	threadCatchupClient
	ConversationsHistory(params map[string]string) (json.RawMessage, error)
}

// ScanResult summarises a channel thread scan.
type ScanResult struct {
	Days             int `json:"days"`
	HistoryMessages  int `json:"history_messages"`
	NewMessages      int `json:"new_messages"`
	NewThreadReplies int `json:"new_thread_replies"`
}

// ScanWindowDays returns the effective scan window for a channel: the larger
// of the requested lookback and the channel's configured deep_scan_days.
func ScanWindowDays(conn *sql.DB, channelID string, lookbackDays int) int {
	days, _ := db.DeepScanDays(conn, channelID)
	if lookbackDays > days {
		return lookbackDays
	}
	return days
}

// ScanChannelThreads re-reads channel history for the last days days, which
// refreshes the authoritative reply_count of every parent in that window
// (including parents archived before their first reply), and then re-fetches
// every thread with replies in the window. Cost: one conversations.history
// call per 200 messages plus one conversations.replies call per thread.
func ScanChannelThreads(conn *sql.DB, client channelScanClient, channelID string, days int) (ScanResult, error) {
	result := ScanResult{Days: days}
	if days <= 0 {
		return result, nil
	}
	oldest := strconv.FormatInt(time.Now().Unix()-int64(days)*86400, 10)
	store := threadStore(conn, client, channelID, &result.NewMessages)

	cursor := ""
	for {
		params := map[string]string{"channel": channelID, "limit": "200", "oldest": oldest}
		if cursor != "" {
			params["cursor"] = cursor
		}
		data, err := client.ConversationsHistory(params)
		if err != nil {
			return result, fmt.Errorf("conversations.history %s: %w", channelID, err)
		}
		var resp struct {
			Messages         []map[string]interface{} `json:"messages"`
			HasMore          bool                     `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return result, fmt.Errorf("parse history %s: %w", channelID, err)
		}
		for _, msg := range resp.Messages {
			if skipHistoryMessage(msg) {
				continue
			}
			result.HistoryMessages++
			store(msg)
		}
		cursor = resp.ResponseMetadata.NextCursor
		if !resp.HasMore || cursor == "" {
			break
		}
	}

	replies, err := catchupThreads(conn, client, days, channelID)
	result.NewThreadReplies = replies
	return result, err
}

// ScanSubscribedChannels runs ScanChannelThreads for every subscribed channel
// whose effective window (max of lookbackDays and deep_scan_days) is positive.
// It stops on authentication errors and reports other per-channel errors.
func ScanSubscribedChannels(conn *sql.DB, client channelScanClient, lookbackDays int) (int, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, ch := range channels {
		days := ScanWindowDays(conn, ch.ID, lookbackDays)
		if days <= 0 {
			continue
		}
		progressf("\n#%s: scanning %d day(s) of history and threads…\n", ch.Name, days)
		result, err := ScanChannelThreads(conn, client, ch.ID, days)
		total += result.NewMessages + result.NewThreadReplies
		if err != nil {
			if slackclient.IsAuthError(err) {
				return total, err
			}
			progressf("  ✗ Error: %v\n", err)
			continue
		}
		progressf("  ✓ %d message(s) re-read, %d new message(s), %d new thread reply(ies)\n",
			result.HistoryMessages, result.NewMessages, result.NewThreadReplies)
	}
	return total, nil
}
