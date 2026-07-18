package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/mattn/go-sqlite3"
)

func init() {
	sql.Register("sqlite3_with_regexp", &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("regexp", func(pattern, s string) (bool, error) {
				return regexp.MatchString("(?i)"+pattern, s)
			}, true)
		},
	})
}

const schema = `
CREATE TABLE IF NOT EXISTS channels (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    subscribed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    name         TEXT,
    real_name    TEXT,
    display_name TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    ts          TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    user_id     TEXT,
    username    TEXT,
    text        TEXT,
    timestamp   REAL NOT NULL,
    thread_ts   TEXT,
    reply_count INTEGER DEFAULT 0,
    raw_json    TEXT,
    PRIMARY KEY (ts, channel_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_channel   ON messages(channel_id);

CREATE TABLE IF NOT EXISTS files (
    id          TEXT PRIMARY KEY,
    ts          TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    name        TEXT,
    mimetype    TEXT,
    url         TEXT,
    local_path  TEXT,
    FOREIGN KEY (ts, channel_id) REFERENCES messages(ts, channel_id)
);

CREATE TABLE IF NOT EXISTS download_state (
    channel_id      TEXT PRIMARY KEY,
    latest_ts       TEXT,
    oldest_ts       TEXT
);
`

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3_with_regexp", path+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func OpenReadonly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3_with_regexp", "file:"+path+"?mode=ro&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("open readonly %s: %w", path, err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(channels)")
	if err != nil {
		return err
	}
	defer rows.Close()

	hasSubscribed := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "subscribed" {
			hasSubscribed = true
		}
	}
	if !hasSubscribed {
		if _, err := db.Exec("ALTER TABLE channels ADD COLUMN subscribed INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		if _, err := db.Exec("UPDATE channels SET subscribed=1 WHERE id IN (SELECT channel_id FROM download_state)"); err != nil {
			return err
		}
	}
	return nil
}

// UpsertChannel inserts or updates a channel, preserving the subscribed flag.
func UpsertChannel(db *sql.DB, id, name string) error {
	_, err := db.Exec(
		`INSERT INTO channels(id, name) VALUES (?, ?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name`,
		id, name,
	)
	return err
}

// SubscribeChannel marks a channel as explicitly downloaded.
func SubscribeChannel(db *sql.DB, channelID string) error {
	_, err := db.Exec("UPDATE channels SET subscribed=1 WHERE id=?", channelID)
	return err
}

// LookupChannelID returns a channel ID by name from the local cache, or "" if not found.
func LookupChannelID(db *sql.DB, name string) string {
	var id string
	err := db.QueryRow("SELECT id FROM channels WHERE name=?", name).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// UpsertUser inserts or replaces a user record from the Slack API payload.
func UpsertUser(db *sql.DB, id, name, realName, displayName string) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO users(id, name, real_name, display_name) VALUES (?, ?, ?, ?)`,
		id, name, realName, displayName,
	)
	return err
}

// MessageExists checks if a message already exists in the database.
func MessageExists(db *sql.DB, ts, channelID string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM messages WHERE ts=? AND channel_id=?", ts, channelID).Scan(&count)
	return count > 0, err
}

// Message represents a Slack message for insertion.
type Message struct {
	TS         string
	ChannelID  string
	UserID     string
	Username   string
	Text       string
	Timestamp  float64
	ThreadTS   string
	ReplyCount int
	RawJSON    json.RawMessage
}

// InsertMessage stores a message. Returns false if it already exists.
func InsertMessage(db *sql.DB, m Message) (bool, error) {
	res, err := db.Exec(
		`INSERT OR IGNORE INTO messages(ts, channel_id, user_id, username, text, timestamp, thread_ts, reply_count, raw_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.TS, m.ChannelID,
		nullStr(m.UserID), nullStr(m.Username),
		nullStr(m.Text), m.Timestamp,
		nullStr(m.ThreadTS), m.ReplyCount,
		nullStr(string(m.RawJSON)),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// File represents a Slack file attachment.
type File struct {
	ID        string
	TS        string
	ChannelID string
	Name      string
	MimeType  string
	URL       string
	LocalPath string
}

// InsertFile stores file metadata.
func InsertFile(db *sql.DB, f File) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO files(id, ts, channel_id, name, mimetype, url, local_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.TS, f.ChannelID,
		nullStr(f.Name), nullStr(f.MimeType),
		nullStr(f.URL), nullStr(f.LocalPath),
	)
	return err
}

// DownloadState holds the incremental download progress for a channel.
type DownloadState struct {
	LatestTS string
	OldestTS string
}

// GetDownloadState returns the stored progress for a channel.
func GetDownloadState(db *sql.DB, channelID string) (DownloadState, error) {
	var s DownloadState
	var latest, oldest sql.NullString
	err := db.QueryRow(
		"SELECT latest_ts, oldest_ts FROM download_state WHERE channel_id=?", channelID,
	).Scan(&latest, &oldest)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.LatestTS = latest.String
	s.OldestTS = oldest.String
	return s, nil
}

// SetDownloadState updates the incremental download progress.
func SetDownloadState(db *sql.DB, channelID, latestTS, oldestTS string) error {
	_, err := db.Exec(
		`INSERT INTO download_state(channel_id, latest_ts, oldest_ts)
		 VALUES (?, ?, ?)
		 ON CONFLICT(channel_id) DO UPDATE SET
		   latest_ts = CASE WHEN excluded.latest_ts > download_state.latest_ts
		                    THEN excluded.latest_ts ELSE download_state.latest_ts END,
		   oldest_ts = CASE WHEN download_state.oldest_ts IS NULL
		                    OR excluded.oldest_ts < download_state.oldest_ts
		                    THEN excluded.oldest_ts ELSE download_state.oldest_ts END`,
		channelID, latestTS, oldestTS,
	)
	return err
}

// SubscribedChannels returns all channels with subscribed=1.
func SubscribedChannels(db *sql.DB) ([]struct{ ID, Name string }, error) {
	rows, err := db.Query("SELECT id, name FROM channels WHERE subscribed=1 ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []struct{ ID, Name string }
	for rows.Next() {
		var ch struct{ ID, Name string }
		if err := rows.Scan(&ch.ID, &ch.Name); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// Channel holds channel info including subscription status.
type Channel struct {
	ID         string `json:"ID"`
	Name       string `json:"Name"`
	Subscribed bool   `json:"Subscribed"`
}

// AllChannelsWithSubscribed returns all channels, subscribed first, then alphabetical.
func AllChannelsWithSubscribed(db *sql.DB) ([]Channel, error) {
	rows, err := db.Query("SELECT id, name, subscribed FROM channels ORDER BY subscribed DESC, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var ch Channel
		var sub int
		if err := rows.Scan(&ch.ID, &ch.Name, &sub); err != nil {
			return nil, err
		}
		ch.Subscribed = sub == 1
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// Stats returns basic archive statistics.
func Stats(db *sql.DB) (messageCount int, channelCount int, oldestMsg, newestMsg time.Time, err error) {
	err = db.QueryRow(`
		SELECT count(*),
		       (SELECT count(*) FROM channels),
		       coalesce(min(timestamp), 0),
		       coalesce(max(timestamp), 0)
		FROM messages
	`).Scan(&messageCount, &channelCount, &oldestMsg, &newestMsg)
	return
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
