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
	LookbackDays     int `json:"lookback_days"`
	DeepScanDays     int `json:"deep_scan_days"`
	HistoryMessages  int `json:"history_messages"`
	NewMessages      int `json:"new_messages"`
	ThreadsFetched   int `json:"threads_fetched"`
	NewThreadReplies int `json:"new_thread_replies"`
}

type archivedThread struct {
	replies   int
	latestRep float64
}

// ScanChannelThreads re-reads channel history for the last
// max(lookbackDays, deepDays) days, which refreshes the authoritative
// reply_count of every parent in that window, including parents archived
// before their first reply. It then fetches threads in two tiers:
//
//   - deep tier, parents from the last deepDays days: every thread with
//     replies is re-fetched, which also captures edits to existing replies;
//   - normal tier, older parents within lookbackDays: a thread is fetched
//     only when Slack reports more replies or a newer latest_reply than the
//     archive holds.
//
// Cost: one conversations.history call per 200 messages in the window, plus
// one conversations.replies call per fetched thread.
func ScanChannelThreads(conn *sql.DB, client channelScanClient, channelID string, lookbackDays, deepDays int) (ScanResult, error) {
	result := ScanResult{LookbackDays: lookbackDays, DeepScanDays: deepDays}
	windowDays := max(lookbackDays, deepDays)
	if windowDays <= 0 {
		return result, nil
	}
	now := time.Now().Unix()
	windowStart := now - int64(windowDays)*86400
	deepStart := now - int64(deepDays)*86400

	archived, err := archivedThreads(conn, channelID, windowStart)
	if err != nil {
		return result, err
	}

	store := threadStore(conn, client, channelID, &result.NewMessages)
	var toFetch []string
	cursor := ""
	for {
		params := map[string]string{
			"channel": channelID,
			"limit":   "200",
			"oldest":  strconv.FormatInt(windowStart, 10),
		}
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
			if ts, fetch := threadNeedsFetch(msg, archived, deepDays > 0, float64(deepStart)); fetch {
				toFetch = append(toFetch, ts)
			}
		}
		cursor = resp.ResponseMetadata.NextCursor
		if !resp.HasMore || cursor == "" {
			break
		}
	}

	replyStore := threadStore(conn, client, channelID, &result.NewThreadReplies)
	for _, ts := range toFetch {
		result.ThreadsFetched++
		if err := fetchReplies(client, channelID, ts, replyStore); err != nil {
			if slackclient.IsAuthError(err) {
				return result, err
			}
			progressf("    ✗ %v\n", err)
		}
	}
	return result, nil
}

// archivedThreads returns, for every thread with archived replies posted
// since windowStart, the number of archived replies and the newest reply ts.
// Replies are newer than their parent, so this covers every parent in the
// window.
func archivedThreads(conn *sql.DB, channelID string, windowStart int64) (map[string]archivedThread, error) {
	rows, err := conn.Query(`
		SELECT thread_ts, COUNT(*), MAX(timestamp)
		FROM messages
		WHERE channel_id = ? AND timestamp >= ?
		  AND thread_ts IS NOT NULL AND thread_ts != '' AND thread_ts != ts
		GROUP BY thread_ts`, channelID, windowStart)
	if err != nil {
		return nil, fmt.Errorf("archived thread stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	threads := map[string]archivedThread{}
	for rows.Next() {
		var threadTS string
		var t archivedThread
		if err := rows.Scan(&threadTS, &t.replies, &t.latestRep); err != nil {
			return nil, fmt.Errorf("archived thread stats: %w", err)
		}
		threads[threadTS] = t
	}
	return threads, rows.Err()
}

// threadNeedsFetch decides whether the thread rooted at a history message
// must be fetched. It returns the parent ts and true when it must.
func threadNeedsFetch(msg map[string]interface{}, archived map[string]archivedThread, deepEnabled bool, deepStart float64) (string, bool) {
	ts, _ := msg["ts"].(string)
	replyCount, _ := msg["reply_count"].(float64)
	if ts == "" || replyCount <= 0 {
		return "", false
	}
	if threadTS, _ := msg["thread_ts"].(string); threadTS != "" && threadTS != ts {
		return "", false // a reply broadcast to the channel, not a parent
	}
	parentTime, _ := strconv.ParseFloat(ts, 64)
	if deepEnabled && parentTime >= deepStart {
		return ts, true
	}
	have := archived[ts]
	if int(replyCount) > have.replies {
		return ts, true
	}
	latestReply, _ := msg["latest_reply"].(string)
	latest, _ := strconv.ParseFloat(latestReply, 64)
	return ts, latest > have.latestRep
}

// ScanSubscribedChannels runs ScanChannelThreads for every subscribed channel
// with the global lookback and the channel's own deep_scan_days. It stops on
// authentication errors and reports other per-channel errors.
func ScanSubscribedChannels(conn *sql.DB, client channelScanClient, lookbackDays int) (int, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, ch := range channels {
		deepDays, err := db.DeepScanDays(conn, ch.ID)
		if err != nil {
			return total, err
		}
		if lookbackDays <= 0 && deepDays <= 0 {
			continue
		}
		progressf("\n#%s: re-scanning %d day(s), deep %d day(s)…\n", ch.Name, max(lookbackDays, deepDays), deepDays)
		result, err := ScanChannelThreads(conn, client, ch.ID, lookbackDays, deepDays)
		total += result.NewMessages + result.NewThreadReplies
		if err != nil {
			if slackclient.IsAuthError(err) {
				return total, err
			}
			progressf("  ✗ Error: %v\n", err)
			continue
		}
		progressf("  ✓ %d message(s) re-read, %d thread(s) fetched, %d new message(s), %d new thread reply(ies)\n",
			result.HistoryMessages, result.ThreadsFetched, result.NewMessages, result.NewThreadReplies)
	}
	return total, nil
}
