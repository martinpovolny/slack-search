package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/martinpovolny/slack-search/internal/format"
	"github.com/martinpovolny/slack-search/internal/search"
)

// Serve starts the MCP server on stdio.
func Serve(db *sql.DB) error {
	s := server.NewMCPServer("slack-search", "1.0.0")

	s.AddTool(toolGrep(), handleGrep(db))
	s.AddTool(toolSQL(), handleSQL(db))
	s.AddTool(toolThread(), handleThread(db))
	s.AddTool(toolChannels(), handleChannels(db))
	s.AddTool(toolSchema(), handleSchema(db))

	return server.ServeStdio(s)
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
			WHERE m.thread_ts = ? AND %s
			ORDER BY m.timestamp
		`, channelClause)

		rows, err := db.Query(query, threadTS, channelArg)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}
		defer rows.Close()

		type msg struct {
			Time   string `json:"time"`
			Author string `json:"author"`
			Text   string `json:"text"`
			TS     string `json:"ts"`
		}
		var msgs []msg
		for rows.Next() {
			var m msg
			if rows.Scan(&m.Time, &m.Author, &m.Text, &m.TS) == nil {
				msgs = append(msgs, m)
			}
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
		mcp.WithDescription("List subscribed Slack channels in the archive (channels with complete message history)."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)
}

func handleChannels(db *sql.DB) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rows, err := db.Query("SELECT id, name FROM channels WHERE subscribed=1 ORDER BY name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer rows.Close()

		type ch struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		var channels []ch
		for rows.Next() {
			var c ch
			if rows.Scan(&c.ID, &c.Name) == nil {
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
		db.QueryRow(
			"SELECT date(min(timestamp),'unixepoch'), date(max(timestamp),'unixepoch') FROM messages",
		).Scan(&oldest, &newest)

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
