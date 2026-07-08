package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

type Options struct {
	FetchThreads bool
	Since        string // Unix timestamp or empty
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
			db.UpsertUser(conn, resp.User.ID, resp.User.Name, resp.User.Profile.RealName, resp.User.Profile.DisplayName)
		}
	}

	// storeMessage returns (isNew, hasNewReplies)
	storeMessage := func(msg map[string]interface{}) (bool, bool) {
		ts, _ := msg["ts"].(string)
		if ts == "" {
			return false, false
		}

		apiReplyCount := 0
		if rc, ok := msg["reply_count"].(float64); ok {
			apiReplyCount = int(rc)
		}

		exists, _ := db.MessageExists(conn, ts, channelID)
		if exists {
			// Check if thread grew since last download
			if apiReplyCount > 0 {
				var storedRC int
				conn.QueryRow("SELECT reply_count FROM messages WHERE ts=? AND channel_id=?", ts, channelID).Scan(&storedRC)
				if apiReplyCount > storedRC {
					conn.Exec("UPDATE messages SET reply_count=? WHERE ts=? AND channel_id=?", apiReplyCount, ts, channelID)
					return false, true
				}
			}
			return false, false
		}

		userID, _ := msg["user"].(string)
		cacheUser(userID)

		username, _ := msg["username"].(string)
		text, _ := msg["text"].(string)
		threadTS, _ := msg["thread_ts"].(string)
		replyCount := 0
		if rc, ok := msg["reply_count"].(float64); ok {
			replyCount = int(rc)
		}

		tsFloat, _ := strconv.ParseFloat(ts, 64)
		rawJSON, _ := json.Marshal(msg)

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
	fmt.Printf("  Downloading #%s (%s)…\n", channelName, label)

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
			Messages []map[string]interface{} `json:"messages"`
			HasMore  bool                     `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return newCount, fmt.Errorf("parse history: %w", err)
		}

		for _, msg := range resp.Messages {
			subtype, _ := msg["subtype"].(string)
			if subtype == "channel_join" || subtype == "channel_leave" {
				continue
			}
			if subtype == "bot_message" {
				t, _ := msg["text"].(string)
				if t == "" {
					continue
				}
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
		fmt.Printf("    Fetching thread %s…\n", threadTS)
		fetchReplies(conn, client, channelID, threadTS, storeMessage)
	}

	// Update download state
	if firstTS != "" && lastTS != "" {
		db.SetDownloadState(conn, channelID, firstTS, lastTS)
	}

	return newCount, nil
}

func fetchReplies(conn *sql.DB, client *slackclient.Client, channelID, threadTS string, store func(map[string]interface{}) (bool, bool)) {
	cursor := ""
	firstPage := true
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
			return
		}

		var resp struct {
			Messages []map[string]interface{} `json:"messages"`
			HasMore  bool                     `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if json.Unmarshal(data, &resp) != nil {
			return
		}

		msgs := resp.Messages
		if firstPage && len(msgs) > 0 {
			msgs = msgs[1:] // skip parent on first page
		}
		firstPage = false

		for _, msg := range msgs {
			store(msg)
		}

		if !resp.HasMore {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
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

// CatchupThreads re-checks threads within the lookback window for new replies.
func CatchupThreads(conn *sql.DB, client *slackclient.Client, lookbackDays int) (int, error) {
	cutoff := float64(time.Now().Unix()) - float64(lookbackDays*86400)

	rows, err := conn.Query(`
		SELECT m.ts, m.channel_id, m.reply_count, c.name
		FROM messages m
		JOIN channels c ON m.channel_id = c.id
		WHERE c.subscribed = 1
		  AND m.timestamp >= ?
		  AND m.reply_count > 0
		  AND (m.thread_ts IS NULL OR m.thread_ts = m.ts)
		ORDER BY m.timestamp DESC
	`, cutoff)
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
			rows.Close()
			return 0, err
		}
		threads = append(threads, t)
	}
	rows.Close()

	if len(threads) == 0 {
		return 0, nil
	}

	fmt.Printf("Checking %d thread(s) in last %d day(s)…\n", len(threads), lookbackDays)
	newCount := 0

	for _, t := range threads {
		var actual int
		conn.QueryRow(
			"SELECT count(*) FROM messages WHERE thread_ts=? AND channel_id=? AND ts!=?",
			t.ts, t.channelID, t.ts,
		).Scan(&actual)

		if actual >= t.storedRC {
			continue
		}

		fmt.Printf("  #%s thread %s: %d/%d replies, fetching…\n", t.channelName, t.ts, actual, t.storedRC)

		storeReply := func(msg map[string]interface{}) (bool, bool) {
			ts, _ := msg["ts"].(string)
			if ts == "" {
				return false, false
			}
			exists, _ := db.MessageExists(conn, ts, t.channelID)
			if exists {
				return false, false
			}
			userID, _ := msg["user"].(string)
			username, _ := msg["username"].(string)
			text, _ := msg["text"].(string)
			threadTS, _ := msg["thread_ts"].(string)
			replyCount := 0
			if rc, ok := msg["reply_count"].(float64); ok {
				replyCount = int(rc)
			}
			tsFloat, _ := strconv.ParseFloat(ts, 64)
			rawJSON, _ := json.Marshal(msg)

			inserted, _ := db.InsertMessage(conn, db.Message{
				TS: ts, ChannelID: t.channelID, UserID: userID, Username: username,
				Text: text, Timestamp: tsFloat, ThreadTS: threadTS,
				ReplyCount: replyCount, RawJSON: rawJSON,
			})
			if inserted {
				newCount++
			}
			return inserted, false
		}

		fetchReplies(conn, client, t.channelID, t.ts, storeReply)
	}

	fmt.Printf("Thread catchup done. %d new reply(ies) found.\n", newCount)
	return newCount, nil
}

// Refresh updates all subscribed channels incrementally.
func Refresh(conn *sql.DB, client *slackclient.Client, opts Options) (int, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return 0, err
	}
	if len(channels) == 0 {
		fmt.Println("No subscribed channels. Run 'download' first.")
		return 0, nil
	}

	totalNew := 0
	for _, ch := range channels {
		fmt.Printf("\n#%s (%s)\n", ch.Name, ch.ID)
		count, err := Download(conn, client, ch.ID, ch.Name, opts)
		if err != nil {
			if slackclient.IsAuthError(err) {
				return totalNew, err
			}
			fmt.Printf("  ✗ Error: %v\n", err)
			continue
		}
		fmt.Printf("  ✓ %d new message(s)\n", count)
		totalNew += count
	}
	fmt.Printf("\nDone. %d new message(s) across %d subscribed channel(s).\n", totalNew, len(channels))
	return totalNew, nil
}
