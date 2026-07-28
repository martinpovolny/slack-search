package download

import (
	"testing"

	"github.com/martinpovolny/slack-search/internal/db"
	_ "github.com/mattn/go-sqlite3"
)

func TestInsertAndSearchCanvases(t *testing.T) {
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Insert a channel first
	if err := db.UpsertChannel(conn, "C456", "test-channel"); err != nil {
		t.Fatal(err)
	}

	// Insert a canvas
	cv := db.Canvas{
		FileID:      "F123abc",
		ChannelID:   "C456",
		QuipID:      "Q789xyz",
		Title:       "Test Canvas Title",
		ContentText: "This is the canvas content for searching",
		ContentHTML: "<p>This is the canvas content for searching</p>",
		UpdatedAt:   1700000000,
	}
	if err := db.InsertCanvas(conn, cv); err != nil {
		t.Fatalf("InsertCanvas: %v", err)
	}

	// Verify it's there
	var count int
	conn.QueryRow("SELECT COUNT(*) FROM canvases WHERE file_id = ?", "F123abc").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 canvas, got %d", count)
	}

	// Search by channel
	results, err := db.SearchCanvases(conn, "test-channel", "")
	if err != nil {
		t.Fatalf("SearchCanvases by channel: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Test Canvas Title" {
		t.Errorf("expected title 'Test Canvas Title', got '%s'", results[0].Title)
	}

	// Search by content query
	results, err = db.SearchCanvases(conn, "", "searching")
	if err != nil {
		t.Fatalf("SearchCanvases by query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for query 'searching', got %d", len(results))
	}

	// Search with no match
	results, err = db.SearchCanvases(conn, "", "nonexistent")
	if err != nil {
		t.Fatalf("SearchCanvases no match: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for 'nonexistent', got %d", len(results))
	}

	// Verify upsert works
	cv.Title = "Updated Canvas Title"
	if err := db.InsertCanvas(conn, cv); err != nil {
		t.Fatalf("InsertCanvas upsert: %v", err)
	}
	var title string
	conn.QueryRow("SELECT title FROM canvases WHERE file_id = ?", "F123abc").Scan(&title)
	if title != "Updated Canvas Title" {
		t.Fatalf("expected 'Updated Canvas Title', got '%s'", title)
	}
}

func TestSearchCanvasesMultipleChannels(t *testing.T) {
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	db.UpsertChannel(conn, "C1", "alpha")
	db.UpsertChannel(conn, "C2", "beta")

	db.InsertCanvas(conn, db.Canvas{
		FileID: "F1", ChannelID: "C1", Title: "Alpha Canvas",
		ContentText: "content alpha", UpdatedAt: 1700000000,
	})
	db.InsertCanvas(conn, db.Canvas{
		FileID: "F2", ChannelID: "C2", Title: "Beta Canvas",
		ContentText: "content beta", UpdatedAt: 1700000001,
	})

	// Search all
	results, err := db.SearchCanvases(conn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 canvases, got %d", len(results))
	}

	// Filter by channel
	results, err = db.SearchCanvases(conn, "alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].FileID != "F1" {
		t.Fatalf("expected 1 canvas from alpha, got %d", len(results))
	}
}
