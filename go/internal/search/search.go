package search

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Result holds the result of a SQL query.
type Result struct {
	Columns []string
	Rows    [][]interface{}
}

// RunSQL executes an arbitrary SQL query and returns the results.
func RunSQL(db *sql.DB, query string) (*Result, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &Result{Columns: cols}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		// Convert []byte to string for JSON serialization
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		result.Rows = append(result.Rows, values)
	}
	return result, rows.Err()
}

// SchemaDescription returns a human-readable description of the database schema.
func SchemaDescription() string {
	return `Tables:
  channels:  id (PK), name, subscribed
  users:     id (PK), name, real_name, display_name
  messages:  ts, channel_id (PK), user_id, username, text, timestamp, thread_ts, reply_count, raw_json
  files:     id (PK), ts, channel_id, name, mimetype, url, local_path
  download_state: channel_id (PK), latest_ts, oldest_ts

Useful joins:
  messages m JOIN users u ON m.user_id = u.id
  messages m JOIN channels c ON m.channel_id = c.id
  files f ON f.ts = m.ts AND f.channel_id = m.channel_id

Date functions:
  datetime(timestamp, 'unixepoch')  — human-readable time
  unixepoch('now', '-7 days')       — 7 days ago as unix timestamp
  strftime('%Y-%W', timestamp, 'unixepoch')  — year-week grouping`
}

// GrepOptions configures a grep search.
type GrepOptions struct {
	FixedString string   // literal search (case-insensitive)
	Pattern     string   // regex pattern (case-insensitive)
	Channels    []string // channel names or IDs
	Since       string   // unix timestamp
	Until       string   // unix timestamp
	Person      string   // partial name match
	Limit       int      // max results
}

// GrepResult holds one grep match.
type GrepResult struct {
	Time     string
	Channel  string
	Author   string
	Text     string
	TS       string
	ThreadTS string
}

// Grep searches messages by text pattern.
func Grep(db *sql.DB, opts GrepOptions) ([]GrepResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 200
	}

	var where []string
	var args []interface{}

	if opts.FixedString != "" {
		where = append(where, "m.text LIKE ?")
		args = append(args, "%"+opts.FixedString+"%")
	}
	if opts.Pattern != "" {
		// Validate regex
		if _, err := regexp.Compile("(?i)" + opts.Pattern); err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		where = append(where, "m.text REGEXP ?")
		args = append(args, opts.Pattern)
	}

	if len(opts.Channels) > 0 {
		placeholders := make([]string, len(opts.Channels))
		for i, ch := range opts.Channels {
			placeholders[i] = "?"
			args = append(args, ch)
		}
		where = append(where, fmt.Sprintf(
			"(c.name IN (%s) OR c.id IN (%s))",
			strings.Join(placeholders, ","),
			strings.Join(placeholders, ","),
		))
		// Duplicate args for the OR
		for _, ch := range opts.Channels {
			args = append(args, ch)
		}
	}

	if opts.Since != "" {
		where = append(where, "m.timestamp >= ?")
		args = append(args, opts.Since)
	}
	if opts.Until != "" {
		where = append(where, "m.timestamp <= ?")
		args = append(args, opts.Until)
	}
	if opts.Person != "" {
		where = append(where, "(u.real_name LIKE ? OR u.display_name LIKE ? OR u.name LIKE ? OR m.username LIKE ?)")
		p := "%" + opts.Person + "%"
		args = append(args, p, p, p, p)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT datetime(m.timestamp, 'unixepoch') as time,
		       c.name as channel,
		       COALESCE(u.real_name, u.display_name, m.username, '') as author,
		       m.text,
		       m.ts,
		       m.thread_ts
		FROM messages m
		JOIN channels c ON m.channel_id = c.id
		LEFT JOIN users u ON m.user_id = u.id
		%s
		ORDER BY m.timestamp DESC
		LIMIT ?
	`, whereClause)
	args = append(args, opts.Limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GrepResult
	for rows.Next() {
		var r GrepResult
		var threadTS sql.NullString
		if err := rows.Scan(&r.Time, &r.Channel, &r.Author, &r.Text, &r.TS, &threadTS); err != nil {
			return nil, err
		}
		r.ThreadTS = threadTS.String
		results = append(results, r)
	}
	return results, rows.Err()
}
