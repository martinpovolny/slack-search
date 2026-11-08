package db

import (
	"bytes"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAppliesDuplicateBlockKitTextMigrationOnce(t *testing.T) {
	var migrationLog bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&migrationLog)
	defer log.SetOutput(previousLogOutput)

	path := filepath.Join(t.TempDir(), "legacy-messages.db")
	legacy, err := sql.Open("sqlite3_with_regexp", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE channels (id TEXT PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE messages (
			ts TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			text TEXT,
			timestamp REAL NOT NULL,
			raw_json TEXT,
			PRIMARY KEY (ts, channel_id)
		);
		INSERT INTO channels(id, name) VALUES ('C_MIGRATION_TEST', 'migration-test');
	`); err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		ts, text, rawJSON string
	}{
		{
			ts:      "1000.000001",
			text:    "echo\necho",
			rawJSON: `{"text":"echo","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"echo"}]}]}]}`,
		},
		{
			ts:      "1001.000001",
			text:    "echo\necho",
			rawJSON: `{"text":"echo"}`,
		},
		{
			ts:      "1002.000001",
			text:    "fallback\nvisible block detail",
			rawJSON: `{"text":"fallback","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"visible block detail"}]}]}]}`,
		},
		{
			ts:      "1003.000001",
			text:    "bad\nbad",
			rawJSON: "{",
		},
	}
	for _, row := range rows {
		if _, err := legacy.Exec(
			"INSERT INTO messages(ts, channel_id, text, timestamp, raw_json) VALUES (?, 'C_MIGRATION_TEST', ?, 1, ?)",
			row.ts, row.text, row.rawJSON,
		); err != nil {
			_ = legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		wantText := row.text
		if i == 0 {
			wantText = "echo"
		}
		assertMessageTextAndRawJSON(t, conn, row.ts, wantText, row.rawJSON)
	}
	assertMigrationRecord(t, conn, 1, "deduplicate-block-kit-message-text", 1)

	// A later open must honor the migration marker rather than rerunning the repair.
	lateText := "late echo\nlate echo"
	lateRaw := `{"text":"late echo","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"late echo"}]}]}]}`
	if _, err := conn.Exec(
		"INSERT INTO messages(ts, channel_id, text, timestamp, raw_json) VALUES ('2000.000001', 'C_MIGRATION_TEST', ?, 2, ?)",
		lateText, lateRaw,
	); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	assertMessageTextAndRawJSON(t, conn, "2000.000001", lateText, lateRaw)
	assertMigrationRecord(t, conn, 1, "deduplicate-block-kit-message-text", 1)
	if got := strings.Count(migrationLog.String(), "Applying database migration 1 (deduplicate-block-kit-message-text)"); got != 1 {
		t.Fatalf("migration start message count = %d, want 1; log: %s", got, migrationLog.String())
	}
}

func assertMessageTextAndRawJSON(t *testing.T, conn *sql.DB, ts, wantText, wantRawJSON string) {
	t.Helper()
	var text, rawJSON string
	if err := conn.QueryRow("SELECT text, raw_json FROM messages WHERE ts=?", ts).Scan(&text, &rawJSON); err != nil {
		t.Fatal(err)
	}
	if text != wantText {
		t.Errorf("message %s text = %q, want %q", ts, text, wantText)
	}
	if rawJSON != wantRawJSON {
		t.Errorf("message %s raw_json changed: got %q, want %q", ts, rawJSON, wantRawJSON)
	}
}

func assertMigrationRecord(t *testing.T, conn *sql.DB, version int, name string, rowsAffected int64) {
	t.Helper()
	var gotName string
	var gotRows int64
	if err := conn.QueryRow(
		"SELECT name, rows_affected FROM schema_migrations WHERE version=?",
		version,
	).Scan(&gotName, &gotRows); err != nil {
		t.Fatal(err)
	}
	if gotName != name || gotRows != rowsAffected {
		t.Errorf("migration record = (%q, %d), want (%q, %d)", gotName, gotRows, name, rowsAffected)
	}
}
