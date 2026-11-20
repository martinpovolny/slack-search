package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/martinpovolny/slack-search/internal/db"
	"github.com/martinpovolny/slack-search/internal/download"
	"github.com/martinpovolny/slack-search/internal/format"
	"github.com/martinpovolny/slack-search/internal/search"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

// Serve starts the MCP server on stdio.
func Serve(db *sql.DB, client *slackclient.Client) error {
	s := server.NewMCPServer("slack-search", "1.0.0", server.WithInstructions(instructions))

	s.AddTool(toolGrep(), handleGrep(db))
	s.AddTool(toolSQL(), handleSQL(db))
	s.AddTool(toolThread(), handleThread(db))
	s.AddTool(toolChannels(), handleChannels(db))
	s.AddTool(toolSchema(), handleSchema(db))
	s.AddTool(toolBookmarks(), handleBookmarks(db))
	s.AddTool(toolCanvases(), handleCanvases(db))
	s.AddTool(toolRefreshChannel(), handleRefreshChannel(db, client))
	s.AddTool(toolFetchThread(), handleFetchThread(db, client))
	s.AddTool(toolSetDeepScan(), handleSetDeepScan(db))

	return server.ServeStdio(s)
}

const instructions = `slack-search tools read a local SQLite archive of Slack channels; only slack_refresh_channel and slack_fetch_thread contact Slack.

Known archive gap: a thread whose parent was archived before its first reply can be missing entirely. Incremental refresh only reads channel history newer than the last stored message, and the thread lookback only re-checks parents whose cached reply_count is above zero, so such a parent stays at reply_count=0 and its replies are never fetched. Symptoms: slack_thread returns no replies, or a permalinked message is absent from slack_grep and slack_sql although its parent is archived.

When a user supplies a Slack permalink, or a thread looks incomplete, call slack_fetch_thread (with the permalink, or channel plus ts) before concluding that a message does not exist, then read it with slack_thread.`

// --- slack_set_deep_scan ---

func toolSetDeepScan() mcp.Tool {
	return mcp.NewTool("slack_set_deep_scan",
		mcp.WithDescription(fmt.Sprintf("Set a channel's deep-scan window: every refresh re-reads that many days of channel history and re-fetches every thread with replies in it, so late replies to older threads are archived. Costs one Slack call per 200 messages plus one per thread in the window. 0 disables it (the global lookback still applies). Maximum %d. Local setting only; does not contact Slack.", db.MaxDeepScanDays)),
		mcp.WithString("channel", mcp.Required(), mcp.Description("Channel name or ID already in the archive")),
		mcp.WithNumber("days", mcp.Required(), mcp.Description("Deep-scan window in days (0 disables)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
}

func handleSetDeepScan(conn *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channel, _ := req.GetArguments()["channel"].(string)
		days, ok := req.GetArguments()["days"].(float64)
		if channel == "" || !ok || days != float64(int(days)) {
			return mcp.NewToolResultError("channel and an integer days are required"), nil
		}
		channelID := db.LookupChannelID(conn, strings.TrimLeft(channel, "#"))
		if channelID == "" {
			channelID = strings.TrimLeft(channel, "#")
		}
		if err := db.SetDeepScanDays(conn, channelID, int(days)); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, _ := json.Marshal(map[string]interface{}{"channel_id": channelID, "deep_scan_days": int(days)})
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_fetch_thread ---

func toolFetchThread() mcp.Tool {
	return mcp.NewTool("slack_fetch_thread",
		mcp.WithDescription("Fetch one Slack thread directly from Slack into the local archive, regardless of cached reply counts. Accepts a message permalink, or a channel plus the ts of the parent or of any reply. Use it when a permalinked message is missing from the archive or slack_thread shows an incomplete thread, then read the thread with slack_thread. Requires ~/.slack-search/.curl credentials."),
		mcp.WithString("permalink", mcp.Description("Slack message permalink, e.g. https://<workspace>.slack.com/archives/C0123/p1700000123456789?thread_ts=1700000000.000100")),
		mcp.WithString("channel", mcp.Description("Channel name or ID (when no permalink is given)")),
		mcp.WithString("ts", mcp.Description("ts of the thread parent or of any reply (when no permalink is given)")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
}

func handleFetchThread(conn *sql.DB, client *slackclient.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if client == nil {
			return mcp.NewToolResultError("Slack credentials are unavailable. Add a valid ~/.slack-search/.curl and restart the MCP server."), nil
		}
		args := req.GetArguments()
		permalink, _ := args["permalink"].(string)
		channel, _ := args["channel"].(string)
		ts, _ := args["ts"].(string)

		var linkedTS string
		if permalink != "" {
			link, err := slackclient.ParsePermalink(permalink)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			channel, ts, linkedTS = link.ChannelID, link.TS, link.TS
			if link.ThreadTS != "" {
				ts = link.ThreadTS
			}
		}
		if channel == "" || ts == "" {
			return mcp.NewToolResultError("provide permalink, or both channel and ts"), nil
		}

		channelID, channelName, err := download.ResolveChannel(client, channel, conn, "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolve channel: %v", err)), nil
		}
		fetched, err := download.FetchThread(conn, client, channelID, ts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetch thread: %v", err)), nil
		}

		result := struct {
			Channel string `json:"channel"`
			download.ThreadFetchResult
			LinkedTS       string `json:"linked_ts,omitempty"`
			LinkedArchived *bool  `json:"linked_message_archived,omitempty"`
		}{Channel: channelName, ThreadFetchResult: fetched, LinkedTS: linkedTS}
		if linkedTS != "" {
			archived, _ := db.MessageExists(conn, linkedTS, channelID)
			result.LinkedArchived = &archived
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_refresh_channel ---

func toolRefreshChannel() mcp.Tool {
	return mcp.NewTool("slack_refresh_channel",
		mcp.WithDescription("Fetch the latest messages for one Slack channel into the local archive, then re-read channel history over the scan window and re-fetch every thread with replies in it, so replies to older parents are captured. The scan window is max(lookback_days, the channel's deep_scan_days). Threads whose parent is older than the window are not re-checked; use slack_fetch_thread for those. Requires ~/.slack-search/.curl credentials."),
		mcp.WithString("channel", mcp.Required(), mcp.Description("Channel name or ID to refresh")),
		mcp.WithNumber("lookback_days", mcp.Description("Days of history and threads to re-scan (default 7, maximum 30); a larger per-channel deep_scan_days takes precedence")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
}

func handleRefreshChannel(conn *sql.DB, client *slackclient.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if client == nil {
			return mcp.NewToolResultError("Slack credentials are unavailable. Add a valid ~/.slack-search/.curl and restart the MCP server."), nil
		}

		channel, _ := req.GetArguments()["channel"].(string)
		if channel == "" {
			return mcp.NewToolResultError("channel is required"), nil
		}

		lookbackDays := 7
		if value, ok := req.GetArguments()["lookback_days"].(float64); ok {
			lookbackDays = int(value)
		}
		if lookbackDays < 1 || lookbackDays > 30 {
			return mcp.NewToolResultError("lookback_days must be between 1 and 30"), nil
		}

		channelID, channelName, err := download.ResolveChannel(client, channel, conn, "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resolve channel: %v", err)), nil
		}

		messageCount, err := download.Download(conn, client, channelID, channelName, download.Options{FetchThreads: true})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("refresh channel: %v", err)), nil
		}
		scanDays := download.ScanWindowDays(conn, channelID, lookbackDays)
		scan, err := download.ScanChannelThreads(conn, client, channelID, scanDays)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("re-scan recent history and threads: %v", err)), nil
		}

		result := struct {
			Channel             string `json:"channel"`
			ChannelID           string `json:"channel_id"`
			NewMessages         int    `json:"new_messages"`
			NewThreadReplies    int    `json:"new_thread_replies"`
			LookbackDays        int    `json:"lookback_days"`
			ScanDays            int    `json:"scan_days"`
			HistoryRescanned    int    `json:"history_messages_rescanned"`
			NewMessagesInRescan int    `json:"new_messages_in_rescan"`
		}{
			Channel:             channelName,
			ChannelID:           channelID,
			NewMessages:         messageCount,
			NewThreadReplies:    scan.NewThreadReplies,
			LookbackDays:        lookbackDays,
			ScanDays:            scanDays,
			HistoryRescanned:    scan.HistoryMessages,
			NewMessagesInRescan: scan.NewMessages,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_grep ---

func toolGrep() mcp.Tool {
	return mcp.NewTool("slack_grep",
		mcp.WithDescription("Search Slack messages by keyword or regex. Returns messages with resolved @mentions."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search text (literal unless regexp=true)")),
		mcp.WithString("channels", mcp.Description("Comma-separated channel names to search in")),
		mcp.WithString("since", mcp.Description("After this date (e.g. '2024-01-01', '3 weeks ago')")),
		mcp.WithString("until", mcp.Description("Before this date")),
		mcp.WithString("person", mcp.Description("Filter by sender name (partial match)")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
		mcp.WithBoolean("regexp", mcp.Description("Treat query as a regular expression")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleGrep(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.GetArguments()["query"].(string)
		channelsStr, _ := req.GetArguments()["channels"].(string)
		since, _ := req.GetArguments()["since"].(string)
		until, _ := req.GetArguments()["until"].(string)
		person, _ := req.GetArguments()["person"].(string)
		limit := 50
		if l, ok := req.GetArguments()["limit"].(float64); ok {
			limit = int(l)
		}
		useRegexp, _ := req.GetArguments()["regexp"].(bool)

		opts := search.GrepOptions{
			Limit:  limit,
			Person: person,
			Since:  since,
			Until:  until,
		}
		if channelsStr != "" {
			opts.Channels = strings.Split(channelsStr, ",")
		}
		if useRegexp {
			opts.Pattern = query
		} else {
			opts.FixedString = query
		}

		results, err := search.Grep(db, opts)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Resolve mentions
		texts := make([]string, len(results))
		for i, r := range results {
			texts[i] = r.Text
		}
		uids := format.ExtractUIDs(texts)
		userMap := format.BuildUserMap(db, uids)
		for i := range results {
			results[i].Text = format.ResolveMentions(results[i].Text, userMap)
		}

		data, _ := json.MarshalIndent(results, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_sql ---

func toolSQL() mcp.Tool {
	return mcp.NewTool("slack_sql",
		mcp.WithDescription("Execute a raw SQL query against the Slack message archive (SQLite). Use slack_schema to see available tables and columns."),
		mcp.WithString("query", mcp.Required(), mcp.Description("SQL query to execute")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleSQL(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.GetArguments()["query"].(string)
		if query == "" {
			return mcp.NewToolResultError("missing query"), nil
		}

		// Block mutations
		upper := strings.ToUpper(strings.TrimSpace(query))
		if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
			return mcp.NewToolResultError("only SELECT queries are allowed"), nil
		}

		result, err := search.RunSQL(db, query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("SQL error: %v", err)), nil
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_thread ---

func toolThread() mcp.Tool {
	return mcp.NewTool("slack_thread",
		mcp.WithDescription("Fetch all messages in a Slack thread by the parent message timestamp. Use this after finding an interesting message via slack_grep to read the full discussion."),
		mcp.WithString("thread_ts", mcp.Required(), mcp.Description("The ts (timestamp) of the parent message")),
		mcp.WithString("channel", mcp.Required(), mcp.Description("Channel name or ID")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleThread(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		threadTS, _ := req.GetArguments()["thread_ts"].(string)
		channel, _ := req.GetArguments()["channel"].(string)
		if threadTS == "" || channel == "" {
			return mcp.NewToolResultError("thread_ts and channel are required"), nil
		}

		// Resolve channel name to ID if needed
		channelClause := "m.channel_id = ?"
		channelArg := channel
		if !strings.HasPrefix(channel, "C") && !strings.HasPrefix(channel, "G") {
			channelClause = "m.channel_id = (SELECT id FROM channels WHERE name = ?)"
		}

		query := fmt.Sprintf(`
			SELECT datetime(m.timestamp, 'unixepoch') as time,
			       COALESCE(u.real_name, u.display_name, m.username, '') as author,
			       m.text,
			       m.ts
			FROM messages m
			LEFT JOIN users u ON m.user_id = u.id
			WHERE (m.thread_ts = ? OR m.ts = ?) AND %s
			ORDER BY m.timestamp
		`, channelClause)

		rows, err := db.Query(query, threadTS, threadTS, channelArg)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}

		type msg struct {
			Time   string `json:"time"`
			Author string `json:"author"`
			Text   string `json:"text"`
			TS     string `json:"ts"`
		}
		var msgs []msg
		for rows.Next() {
			var m msg
			if err := rows.Scan(&m.Time, &m.Author, &m.Text, &m.TS); err != nil {
				if closeErr := rows.Close(); closeErr != nil {
					err = fmt.Errorf("scan message row: %w (close rows: %v)", err, closeErr)
				}
				return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
			}
			msgs = append(msgs, m)
		}
		if err := rows.Err(); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				err = fmt.Errorf("read message rows: %w (close rows: %v)", err, closeErr)
			}
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		if err := rows.Close(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query close error: %v", err)), nil
		}

		// Resolve mentions
		texts := make([]string, len(msgs))
		for i, m := range msgs {
			texts[i] = m.Text
		}
		uids := format.ExtractUIDs(texts)
		userMap := format.BuildUserMap(db, uids)
		for i := range msgs {
			msgs[i].Text = format.ResolveMentions(msgs[i].Text, userMap)
		}

		if len(msgs) == 0 {
			return mcp.NewToolResultText("No messages found in this thread."), nil
		}

		data, _ := json.MarshalIndent(msgs, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_channels ---

func toolChannels() mcp.Tool {
	return mcp.NewTool("slack_channels",
		mcp.WithDescription("List subscribed Slack channels in the archive (channels with complete message history). deep_scan_days, when present, is the channel's per-channel history and thread re-scan window."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleChannels(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rows, err := db.Query("SELECT id, name, deep_scan_days FROM channels WHERE subscribed=1 ORDER BY name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer func() {
			_ = rows.Close()
		}()

		type ch struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			DeepScanDays int    `json:"deep_scan_days,omitempty"`
		}
		var channels []ch
		for rows.Next() {
			var c ch
			if rows.Scan(&c.ID, &c.Name, &c.DeepScanDays) == nil {
				channels = append(channels, c)
			}
		}

		data, _ := json.MarshalIndent(channels, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_schema ---

func toolSchema() mcp.Tool {
	return mcp.NewTool("slack_schema",
		mcp.WithDescription("Get the database schema, useful joins, and SQLite date function cheatsheet. Call this before writing SQL with slack_sql."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleSchema(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		schema := search.SchemaDescription()

		// Add archive date range
		var oldest, newest sql.NullString
		if err := db.QueryRow(
			"SELECT date(min(timestamp),'unixepoch'), date(max(timestamp),'unixepoch') FROM messages",
		).Scan(&oldest, &newest); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("archive date range query: %v", err)), nil
		}

		if oldest.Valid {
			schema += fmt.Sprintf("\n\nArchive date range: %s to %s", oldest.String, newest.String)
		}

		// Add SQLite gotchas
		schema += `

SQLite gotchas:
  - Use LIKE not ILIKE (already case-insensitive)
  - Use unixepoch('now') not NOW()
  - Use unixepoch('now', '-7 days') not INTERVAL
  - Use timestamp >= unixepoch('2024-01-01') not datetime(...) >= '2024-...'
  - Thread parents have thread_ts = ts (not NULL)
  - User names: search all three columns (name, real_name, display_name)
  - Mentions in text are encoded as <@UXXXXXXX>`

		return mcp.NewToolResultText(schema), nil
	}
}

// --- slack_bookmarks ---

func toolBookmarks() mcp.Tool {
	return mcp.NewTool("slack_bookmarks",
		mcp.WithDescription("List bookmarks from subscribed Slack channels. Returns titles, links, and associated channels."),
		mcp.WithString("channel", mcp.Description("Filter by channel name (optional)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleBookmarks(conn *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channel, _ := req.GetArguments()["channel"].(string)

		query := `SELECT b.id, b.channel_id, c.name as channel_name, b.title, b.link, b.type, b.emoji,
		          datetime(b.created_at, 'unixepoch') as created
		          FROM bookmarks b JOIN channels c ON b.channel_id = c.id`
		var args []interface{}
		if channel != "" {
			query += " WHERE c.name = ?"
			args = append(args, channel)
		}
		query += " ORDER BY c.name, b.title"

		rows, err := conn.Query(query, args...)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close() //nolint:errcheck

		type bookmark struct {
			ID          string `json:"id"`
			ChannelID   string `json:"channel_id"`
			ChannelName string `json:"channel_name"`
			Title       string `json:"title"`
			Link        string `json:"link"`
			Type        string `json:"type"`
			Emoji       string `json:"emoji"`
			Created     string `json:"created"`
		}
		var bookmarks []bookmark
		for rows.Next() {
			var bm bookmark
			var title, link, typ, emoji, created sql.NullString
			if err := rows.Scan(&bm.ID, &bm.ChannelID, &bm.ChannelName, &title, &link, &typ, &emoji, &created); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("scan error: %v", err)), nil
			}
			bm.Title = title.String
			bm.Link = link.String
			bm.Type = typ.String
			bm.Emoji = emoji.String
			bm.Created = created.String
			bookmarks = append(bookmarks, bm)
		}

		if len(bookmarks) == 0 {
			return mcp.NewToolResultText("No bookmarks found."), nil
		}

		data, _ := json.MarshalIndent(bookmarks, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}

// --- slack_canvases ---

func toolCanvases() mcp.Tool {
	return mcp.NewTool("slack_canvases",
		mcp.WithDescription("Search Slack canvases by channel or content. Returns titles and text content."),
		mcp.WithString("channel", mcp.Description("Filter by channel name (optional)")),
		mcp.WithString("query", mcp.Description("Search canvas content (optional)")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleCanvases(conn *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		channel, _ := req.GetArguments()["channel"].(string)
		query, _ := req.GetArguments()["query"].(string)

		canvases, err := db.SearchCanvases(conn, channel, query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}

		if len(canvases) == 0 {
			return mcp.NewToolResultText("No canvases found."), nil
		}

		type canvasResult struct {
			FileID      string `json:"file_id"`
			ChannelID   string `json:"channel_id"`
			QuipID      string `json:"quip_id"`
			Title       string `json:"title"`
			ContentText string `json:"content_text"`
		}

		results := make([]canvasResult, len(canvases))
		for i, cv := range canvases {
			results[i] = canvasResult{
				FileID:      cv.FileID,
				ChannelID:   cv.ChannelID,
				QuipID:      cv.QuipID,
				Title:       cv.Title,
				ContentText: cv.ContentText,
			}
		}

		data, _ := json.MarshalIndent(results, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
}
