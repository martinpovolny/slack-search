package mcpserver

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/martinpovolny/slack-search/internal/db"
)

func TestNewServerRegistersTools(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	got := newServer(conn, nil).ListTools()
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)

	want := []string{
		"slack_bookmarks", "slack_canvases", "slack_channels", "slack_grep",
		"slack_refresh_channel", "slack_schema", "slack_sql", "slack_thread",
	}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
}
