package download

import (
	"testing"

	"github.com/martinpovolny/slack-search/internal/db"
	_ "github.com/mattn/go-sqlite3"
)

func TestInsertAndListBookmarks(t *testing.T) {
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Insert a channel first
	if err := db.UpsertChannel(conn, "C123", "test-channel"); err != nil {
		t.Fatal(err)
	}

	// Insert a bookmark
	bm := db.Bookmark{
		ID:        "Bk123",
		ChannelID: "C123",
		Title:     "Test Bookmark",
		Link:      "https://example.com",
		Type:      "link",
		Emoji:     ":bookmark:",
		CreatedAt: 1700000000,
	}
	if err := db.InsertBookmark(conn, bm); err != nil {
		t.Fatalf("InsertBookmark: %v", err)
	}

	// Verify it's there
	var count int
	conn.QueryRow("SELECT COUNT(*) FROM bookmarks WHERE id = ?", "Bk123").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 bookmark, got %d", count)
	}

	// Verify upsert works (INSERT OR REPLACE)
	bm.Title = "Updated Title"
	if err := db.InsertBookmark(conn, bm); err != nil {
		t.Fatalf("InsertBookmark upsert: %v", err)
	}
	var title string
	conn.QueryRow("SELECT title FROM bookmarks WHERE id = ?", "Bk123").Scan(&title)
	if title != "Updated Title" {
		t.Fatalf("expected 'Updated Title', got '%s'", title)
	}
}
