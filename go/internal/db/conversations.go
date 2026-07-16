package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const convSchema = `
CREATE TABLE IF NOT EXISTS conversations (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT 'New conversation',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_user ON conversations(user_id, updated_at);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    sql_text        TEXT,
    result_json     TEXT,
    created_at      REAL NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_msg_conv ON messages(conversation_id, created_at);
`

// OpenConversationsDB opens or creates the conversations database.
func OpenConversationsDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("open conversations db %s: %w", path, err)
	}
	if _, err := db.Exec(convSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init conversations schema: %w", err)
	}
	migrateConvDB(db)
	return db, nil
}

func migrateConvDB(db *sql.DB) {
	rows, _ := db.Query("PRAGMA table_info(messages)")
	if rows == nil {
		return
	}
	defer rows.Close()
	has := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if name == "result_json" {
			has = true
		}
	}
	if !has {
		db.Exec("ALTER TABLE messages ADD COLUMN result_json TEXT")
	}
}

// Conversation represents a stored conversation.
type Conversation struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	UpdatedAt float64 `json:"updated_at"`
}

// ListConversations returns all conversations, newest first.
func ListConversations(db *sql.DB) ([]Conversation, error) {
	rows, err := db.Query(
		"SELECT id, title, updated_at FROM conversations ORDER BY updated_at DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.UpdatedAt); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

// CreateConversation creates a new conversation and returns its ID.
func CreateConversation(db *sql.DB) (string, error) {
	id := uuid.New().String()
	now := float64(time.Now().UnixMilli()) / 1000.0
	_, err := db.Exec(
		"INSERT INTO conversations(id, user_id, title, created_at, updated_at) VALUES (?,?,?,?,?)",
		id, "default", "New conversation", now, now,
	)
	return id, err
}

// RenameConversation updates a conversation's title.
func RenameConversation(db *sql.DB, id, title string) error {
	_, err := db.Exec("UPDATE conversations SET title=? WHERE id=?", title, id)
	return err
}

// DeleteConversation removes a conversation and its messages.
func DeleteConversation(db *sql.DB, id string) error {
	_, err := db.Exec("DELETE FROM conversations WHERE id=?", id)
	return err
}

// ConvMessage represents a message in a conversation.
type ConvMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	SQL        string          `json:"sql,omitempty"`
	ResultJSON json.RawMessage `json:"result,omitempty"`
}

// LoadConvMessages returns all messages in a conversation.
func LoadConvMessages(db *sql.DB, conversationID string) ([]ConvMessage, error) {
	rows, err := db.Query(
		"SELECT role, content, sql_text, result_json FROM messages WHERE conversation_id=? ORDER BY created_at",
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ConvMessage
	for rows.Next() {
		var m ConvMessage
		var sqlText, resultJSON sql.NullString
		if err := rows.Scan(&m.Role, &m.Content, &sqlText, &resultJSON); err != nil {
			return nil, err
		}
		m.SQL = sqlText.String
		if resultJSON.Valid && resultJSON.String != "" {
			m.ResultJSON = json.RawMessage(resultJSON.String)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// AppendConvMessage adds a message to a conversation.
func AppendConvMessage(db *sql.DB, conversationID, role, content, sqlText, resultJSON string) error {
	now := float64(time.Now().UnixMilli()) / 1000.0
	_, err := db.Exec(
		"INSERT INTO messages(conversation_id, role, content, sql_text, result_json, created_at) VALUES (?,?,?,?,?,?)",
		conversationID, role, content, nullStr(sqlText), nullStr(resultJSON), now,
	)
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE conversations SET updated_at=? WHERE id=?", now, conversationID)
	return err
}

// AutoTitle derives a short title from the first user message.
func AutoTitle(msg string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 60
	}
	lines := []byte(msg)
	// Take first line
	for i, b := range lines {
		if b == '\n' {
			lines = lines[:i]
			break
		}
	}
	t := string(lines)
	if len(t) <= maxLen {
		return t
	}
	return t[:maxLen-1] + "…"
}
