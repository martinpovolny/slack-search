package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

// Out receives human-readable progress output. The MCP server redirects it to
// stderr so progress lines cannot corrupt the stdio protocol stream.
var Out io.Writer = os.Stdout

// progressf writes a progress line to Out. Progress output is best effort.
func progressf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(Out, format, args...)
}

type Options struct {
	FetchThreads bool
	Since        string // Unix timestamp or empty
}

// messageText returns the searchable text for a Slack message. Some Slack
// integrations, including Alertmanager, put the useful content in an
// attachment while leaving the top-level text empty.
func messageText(msg map[string]interface{}) string {
	var parts []string
	appendText := func(target *[]string, value interface{}) {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			*target = append(*target, strings.TrimSpace(text))
		}
	}

	appendText(&parts, msg["text"])
	if attachments, ok := msg["attachments"].([]interface{}); ok {
		for _, attachment := range attachments {
			if item, ok := attachment.(map[string]interface{}); ok {
				appendText(&parts, item["fallback"])
				appendText(&parts, item["pretext"])
				appendText(&parts, item["title"])
				appendText(&parts, item["text"])
				if fields, ok := item["fields"].([]interface{}); ok {
					for _, field := range fields {
						if value, ok := field.(map[string]interface{}); ok {
							appendText(&parts, value["title"])
							appendText(&parts, value["value"])
						}
					}
				}
			}
		}
	}

	// Block Kit messages commonly carry text in nested objects. Walk only
	// text-bearing keys so IDs and formatting metadata do not pollute search.
	var blockParts []string
	var appendBlocks func(interface{})
	appendBlocks = func(value interface{}) {
		switch value := value.(type) {
		case []interface{}:
			for _, item := range value {
				appendBlocks(item)
			}
		case map[string]interface{}:
			if kind, _ := value["type"].(string); kind == "text" || kind == "plain_text" || kind == "mrkdwn" {
				appendText(&blockParts, value["text"])
			}
			for _, key := range []string{"blocks", "elements", "fields"} {
				appendBlocks(value[key])
			}
		}
	}
	appendBlocks(msg["blocks"])

	// Slack can provide the same message body in both the top-level fallback
	// and a rich-text block. Keep one copy when the full block text is identical.
	topLevelText, _ := msg["text"].(string)
	blockText := strings.Join(blockParts, "\n")
	if strings.TrimSpace(topLevelText) != blockText {
		parts = append(parts, blockParts...)
	}

	return strings.Join(parts, "\n")
}

// repliesClient is the portion of the Slack client needed to fetch a thread.
// Keeping this narrow lets thread reconciliation be exercised without Slack
// credentials.
type repliesClient interface {
	ConversationsReplies(params map[string]string) (json.RawMessage, error)
}

type threadCatchupClient interface {
	repliesClient
	UsersInfo(userID string) (json.RawMessage, error)
}

// Download fetches messages from a Slack channel and stores them. Returns count of new messages.
func Download(conn *sql.DB, client *slackclient.Client, channelID, channelName string, opts Options) (int, error) {
	if err := db.UpsertChannel(conn, channelID, channelName); err != nil {
		return 0, err
	}
	if err := db.SubscribeChannel(conn, channelID); err != nil {
		return 0, err
	}

	state, err := db.GetDownloadState(conn, channelID)
	if err != nil {
		return 0, err
	}

	seenUsers := map[string]bool{}
	newCount := 0

	cacheUser := func(userID string) {
		if userID == "" || seenUsers[userID] {
			return
		}
		seenUsers[userID] = true
		data, err := client.UsersInfo(userID)
		if err != nil {
			return
		}
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
			_ = db.UpsertUser(conn, resp.User.ID, resp.User.Name, resp.User.Profile.RealName, resp.User.Profile.DisplayName)
		}
	}

	// storeMessage returns (isNew, hasNewReplies)
	storeMessage := func(msg map[string]interface{}) (bool, bool) {
		ts, _ := msg["ts"].(string)
		if ts == "" {
			return false, false
		}
		text := messageText(msg)
		rawJSON, _ := json.Marshal(msg)

		apiReplyCount := 0
		if rc, ok := msg["reply_count"].(float64); ok {
			apiReplyCount = int(rc)
		}

		exists, _ := db.MessageExists(conn, ts, channelID)
		if exists {
			// live-search may have cached only a lightweight search result. A
			// subsequent history download can contain the full attachment or
			// block payload, so reconcile the richer representation.
			if text != "" {
				_, _ = conn.Exec(
					"UPDATE messages SET text=?, raw_json=? WHERE ts=? AND channel_id=?",
					text, rawJSON, ts, channelID,
				)
			}
			// Check if thread grew since last download
			if apiReplyCount > 0 {
				var storedRC int
				_ = conn.QueryRow("SELECT reply_count FROM messages WHERE ts=? AND channel_id=?", ts, channelID).Scan(&storedRC)
				if apiReplyCount > storedRC {
					_, _ = conn.Exec("UPDATE messages SET reply_count=? WHERE ts=? AND channel_id=?", apiReplyCount, ts, channelID)
					return false, true
				}
			}
			return false, false
		}

		userID, _ := msg["user"].(string)
		cacheUser(userID)

		username, _ := msg["username"].(string)
		threadTS, _ := msg["thread_ts"].(string)
		replyCount := 0
		if rc, ok := msg["reply_count"].(float64); ok {
			replyCount = int(rc)
		}

		tsFloat, _ := strconv.ParseFloat(ts, 64)
		inserted, err := db.InsertMessage(conn, db.Message{
			TS:         ts,
			ChannelID:  channelID,
			UserID:     userID,
			Username:   username,
			Text:       text,
			Timestamp:  tsFloat,
			ThreadTS:   threadTS,
			ReplyCount: replyCount,
			RawJSON:    rawJSON,
		})
		if err != nil || !inserted {
			return false, false
		}
		newCount++
		return true, false
	}

	// Determine oldest argument for pagination
	oldest := opts.Since
	if oldest == "" && state.LatestTS != "" {
		oldest = state.LatestTS
	}

	label := "all history"
	if oldest != "" {
		label = "new messages since last run"
	}
	progressf("  Downloading #%s (%s)…\n", channelName, label)

	var firstTS, lastTS string
	threadsToFetch := []string{}

	// Paginate through history
	cursor := ""
	for {
		params := map[string]string{
			"channel": channelID,
			"limit":   "200",
		}
		if oldest != "" {
			params["oldest"] = oldest
			params["inclusive"] = "false"
		}
		if cursor != "" {
			params["cursor"] = cursor
		}

		data, err := client.ConversationsHistory(params)
		if err != nil {
			return newCount, err
		}

		var resp struct {
			Messages         []map[string]interface{} `json:"messages"`
			HasMore          bool                     `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return newCount, fmt.Errorf("parse history: %w", err)
		}

		for _, msg := range resp.Messages {
			if skipHistoryMessage(msg) {
				continue
			}

			ts, _ := msg["ts"].(string)
			if firstTS == "" {
				firstTS = ts
			}
			lastTS = ts

			isNew, hasNewReplies := storeMessage(msg)

			if opts.FetchThreads {
				threadTS, _ := msg["thread_ts"].(string)
				rc, _ := msg["reply_count"].(float64)

				if rc > 0 && (isNew || hasNewReplies) {
					if threadTS == "" {
						threadTS = ts
					}
					threadsToFetch = append(threadsToFetch, threadTS)
				} else if isNew && threadTS != "" && threadTS != ts {
					// New reply to an existing thread — fetch the full thread
					threadsToFetch = append(threadsToFetch, threadTS)
				}
			}
		}

		if !resp.HasMore {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}

	// Fetch thread replies
	for _, threadTS := range threadsToFetch {
		progressf("    Fetching thread %s…\n", threadTS)
		if err := fetchReplies(client, channelID, threadTS, storeMessage); err != nil {
			if slackclient.IsAuthError(err) {
				return newCount, err
			}
			progressf("    ✗ thread %s: %v\n", threadTS, err)
		}
	}

	// Update download state
	if firstTS != "" && lastTS != "" {
		_ = db.SetDownloadState(conn, channelID, firstTS, lastTS)
	}

	return newCount, nil
}

// skipHistoryMessage reports whether a history message is noise the archive
// does not keep: join/leave notices and empty bot posts.
func skipHistoryMessage(msg map[string]interface{}) bool {
	subtype, _ := msg["subtype"].(string)
	if subtype == "channel_join" || subtype == "channel_leave" {
		return true
	}
	return subtype == "bot_message" && messageText(msg) == ""
}

// fetchReplies pages through conversations.replies and passes the parent and
// every reply to store. The first message of the first page is the parent.
func fetchReplies(client repliesClient, channelID, threadTS string, store func(map[string]interface{}) (bool, bool)) error {
	cursor := ""
	for {
		params := map[string]string{
			"channel": channelID,
			"ts":      threadTS,
			"limit":   "200",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}

		data, err := client.ConversationsReplies(params)
		if err != nil {
			return fmt.Errorf("conversations.replies %s: %w", threadTS, err)
		}

		var resp struct {
			Messages         []map[string]interface{} `json:"messages"`
			HasMore          bool                     `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("parse replies %s: %w", threadTS, err)
		}

		// The parent is re-stored too, so its reply count is refreshed from
		// the authoritative thread response.
		for _, msg := range resp.Messages {
			store(msg)
		}

		if !resp.HasMore {
			return nil
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			return nil
		}
	}
}

// ThreadFetchResult summarises a single-thread fetch.
type ThreadFetchResult struct {
	ChannelID   string `json:"channel_id"`
	ThreadTS    string `json:"thread_ts"`
	ReplyCount  int    `json:"reply_count"`
	Fetched     int    `json:"fetched_messages"`
	NewMessages int    `json:"new_messages"`
}

// FetchThread fetches one thread from Slack and stores the parent and all of
// its replies, regardless of the cached reply_count. ts may be the parent ts
// or the ts of any reply in the thread; Slack returns the whole thread either
// way. Use it to backfill a thread referenced by a permalink.
func FetchThread(conn *sql.DB, client threadCatchupClient, channelID, ts string) (ThreadFetchResult, error) {
	result := ThreadFetchResult{ChannelID: channelID, ThreadTS: ts}
	if channelID == "" || ts == "" {
		return result, fmt.Errorf("channel ID and message ts are required")
	}
	store := threadStore(conn, client, channelID, &result.NewMessages)
	err := fetchReplies(client, channelID, ts, func(msg map[string]interface{}) (bool, bool) {
		result.Fetched++
		if threadTS, _ := msg["thread_ts"].(string); threadTS != "" {
			result.ThreadTS = threadTS
		}
		if rc, ok := msg["reply_count"].(float64); ok {
			result.ReplyCount = int(rc)
		}
		return store(msg)
	})
	return result, err
}

// threadStore returns a store callback for conversations.replies messages.
// Existing messages get their text, raw JSON and reply count refreshed; new
// messages are inserted and counted in newCount. Authors are cached on first
// sight so formatted threads show names rather than user IDs.
func threadStore(conn *sql.DB, client threadCatchupClient, channelID string, newCount *int) func(map[string]interface{}) (bool, bool) {
	seenUsers := map[string]bool{}
	return func(msg map[string]interface{}) (bool, bool) {
		ts, _ := msg["ts"].(string)
		if ts == "" {
			return false, false
		}
		text := messageText(msg)
		rawJSON, _ := json.Marshal(msg)
		_, hasReplyCount := msg["reply_count"].(float64)
		replyCount := 0
		if rc, ok := msg["reply_count"].(float64); ok {
			replyCount = int(rc)
		}

		threadTS, _ := msg["thread_ts"].(string)
		exists, _ := db.MessageExists(conn, ts, channelID)
		if exists {
			if threadTS != "" {
				// A parent archived before its first reply has no thread_ts.
				_, _ = conn.Exec(
					"UPDATE messages SET thread_ts=? WHERE ts=? AND channel_id=? AND COALESCE(thread_ts, '')=''",
					threadTS, ts, channelID,
				)
			}
			if hasReplyCount {
				_, _ = conn.Exec(
					"UPDATE messages SET reply_count=?, raw_json=? WHERE ts=? AND channel_id=?",
					replyCount, rawJSON, ts, channelID,
				)
			}
			if text != "" {
				_, _ = conn.Exec(
					"UPDATE messages SET text=?, raw_json=? WHERE ts=? AND channel_id=?",
					text, rawJSON, ts, channelID,
				)
			}
			return false, false
		}

		userID, _ := msg["user"].(string)
		if userID != "" && !seenUsers[userID] {
			seenUsers[userID] = true
			cacheUserIfMissing(conn, client, userID)
		}
		username, _ := msg["username"].(string)
		tsFloat, _ := strconv.ParseFloat(ts, 64)

		inserted, _ := db.InsertMessage(conn, db.Message{
			TS: ts, ChannelID: channelID, UserID: userID, Username: username,
			Text: text, Timestamp: tsFloat, ThreadTS: threadTS,
			ReplyCount: replyCount, RawJSON: rawJSON,
		})
		if inserted {
			*newCount++
		}
		return inserted, false
	}
}

func cacheUserIfMissing(conn *sql.DB, client threadCatchupClient, userID string) {
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM users WHERE id=?", userID).Scan(&n); err == nil && n > 0 {
		return
	}
	data, err := client.UsersInfo(userID)
	if err != nil {
		return
	}
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
	if json.Unmarshal(data, &resp) == nil && resp.User.ID != "" {
		_ = db.UpsertUser(conn, resp.User.ID, resp.User.Name, resp.User.Profile.RealName, resp.User.Profile.DisplayName)
	}
}

// ResolveChannel resolves a channel name or ID to (channelID, channelName).
func ResolveChannel(client *slackclient.Client, channel string, conn *sql.DB, hintID string) (string, string, error) {
	stripped := strings.TrimLeft(channel, "#")

	// Direct ID
	if (strings.HasPrefix(stripped, "C") || strings.HasPrefix(stripped, "G") || strings.HasPrefix(stripped, "D")) && len(stripped) > 8 {
		data, err := client.ConversationsInfo(stripped)
		if err != nil {
			return "", "", err
		}
		var resp struct {
			Channel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
		}
		if json.Unmarshal(data, &resp) == nil {
			name := resp.Channel.Name
			if name == "" {
				name = stripped
			}
			return resp.Channel.ID, name, nil
		}
	}

	// DB cache
	if conn != nil {
		if id := db.LookupChannelID(conn, stripped); id != "" {
			return id, stripped, nil
		}
	}

	// Hint ID from curl
	if hintID != "" {
		data, err := client.ConversationsInfo(hintID)
		if err == nil {
			var resp struct {
				Channel struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
			}
			if json.Unmarshal(data, &resp) == nil {
				if resp.Channel.Name == stripped || stripped == "" {
					return resp.Channel.ID, resp.Channel.Name, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("channel '%s' not found — use the channel ID directly (e.g. C04476G1F7H)", channel)
}

// CatchupThreads re-fetches all subscribed threads within the lookback window.
func CatchupThreads(conn *sql.DB, client threadCatchupClient, lookbackDays int) (int, error) {
	return catchupThreads(conn, client, lookbackDays, "")
}

// CatchupChannelThreads re-fetches threads in one channel within the lookback
// window. It is useful after an incremental channel refresh, where the cached
// parent reply_count may otherwise hide a recently added reply.
func CatchupChannelThreads(conn *sql.DB, client threadCatchupClient, channelID string, lookbackDays int) (int, error) {
	if channelID == "" {
		return 0, fmt.Errorf("channel ID is required")
	}
	return catchupThreads(conn, client, lookbackDays, channelID)
}

// catchupThreads re-fetches threaded parents instead of trusting reply_count.
// A cached parent reply_count is only a snapshot from conversations.history;
// it does not reliably change when a later reply is posted.
func catchupThreads(conn *sql.DB, client threadCatchupClient, lookbackDays int, channelID string) (int, error) {
	cutoff := float64(time.Now().Unix()) - float64(lookbackDays*86400)

	query := `
		SELECT m.ts, m.channel_id, m.reply_count, c.name
		FROM messages m
		JOIN channels c ON m.channel_id = c.id
		WHERE c.subscribed = 1
		  AND m.timestamp >= ?
		  AND m.reply_count > 0
		  AND (m.thread_ts IS NULL OR m.thread_ts = m.ts)
	`
	args := []interface{}{cutoff}
	if channelID != "" {
		query += " AND m.channel_id = ?"
		args = append(args, channelID)
	}
	query += " ORDER BY m.timestamp DESC"

	rows, err := conn.Query(query, args...)
	if err != nil {
		return 0, err
	}

	type threadInfo struct {
		ts, channelID, channelName string
		storedRC                   int
	}
	var threads []threadInfo
	for rows.Next() {
		var t threadInfo
		if err := rows.Scan(&t.ts, &t.channelID, &t.storedRC, &t.channelName); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				return 0, fmt.Errorf("scan thread row: %w (close rows: %v)", err, closeErr)
			}
			return 0, err
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		if closeErr := rows.Close(); closeErr != nil {
			return 0, fmt.Errorf("iterate thread rows: %w (close rows: %v)", err, closeErr)
		}
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	if len(threads) == 0 {
		return 0, nil
	}

	progressf("Checking %d thread(s) in last %d day(s)…\n", len(threads), lookbackDays)
	newCount := 0

	for _, t := range threads {
		progressf("  #%s thread %s: re-fetching (cached %d replies)…\n", t.channelName, t.ts, t.storedRC)

		store := threadStore(conn, client, t.channelID, &newCount)
		if err := fetchReplies(client, t.channelID, t.ts, store); err != nil {
			if slackclient.IsAuthError(err) {
				return newCount, err
			}
			progressf("    ✗ %v\n", err)
		}
	}

	progressf("Thread catchup done. %d new reply(ies) found.\n", newCount)
	return newCount, nil
}

// Refresh updates all subscribed channels incrementally.
func Refresh(conn *sql.DB, client *slackclient.Client, opts Options) (int, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return 0, err
	}
	if len(channels) == 0 {
		progressf("No subscribed channels. Run 'download' first.\n")
		return 0, nil
	}

	totalNew := 0
	for _, ch := range channels {
		progressf("\n#%s (%s)\n", ch.Name, ch.ID)
		count, err := Download(conn, client, ch.ID, ch.Name, opts)
		if err != nil {
			if slackclient.IsAuthError(err) {
				return totalNew, err
			}
			progressf("  ✗ Error: %v\n", err)
			continue
		}
		progressf("  ✓ %d new message(s)\n", count)
		totalNew += count
	}
	progressf("\nDone. %d new message(s) across %d subscribed channel(s).\n", totalNew, len(channels))
	return totalNew, nil
}
