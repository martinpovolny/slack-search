package download

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
)

type fakeThreadClient struct {
	replies json.RawMessage
	calls   int
}

func (c *fakeThreadClient) ConversationsReplies(map[string]string) (json.RawMessage, error) {
	c.calls++
	return c.replies, nil
}

func (c *fakeThreadClient) UsersInfo(string) (json.RawMessage, error) {
	return json.RawMessage(`{"ok": true}`), nil
}

func insertTestMessage(t *testing.T, conn *sql.DB, message db.Message) {
	t.Helper()
	inserted, err := db.InsertMessage(conn, message)
	if err != nil || !inserted {
		t.Fatalf("insert message %s: inserted=%v err=%v", message.TS, inserted, err)
	}
}

func TestCatchupThreadsRefreshesThreadWhenCachedReplyCountMatches(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const channelID = "C12345678"
	const parentTS = "1000.000001"
	if err := db.UpsertChannel(conn, channelID, "test-channel"); err != nil {
		t.Fatal(err)
	}
	if err := db.SubscribeChannel(conn, channelID); err != nil {
		t.Fatal(err)
	}

	now := float64(time.Now().Unix())
	insertTestMessage(t, conn, db.Message{TS: parentTS, ChannelID: channelID, Timestamp: now, ThreadTS: parentTS, ReplyCount: 3, RawJSON: json.RawMessage(`{"ts":"1000.000001","reply_count":3}`)})
	for _, ts := range []string{"1001.000001", "1002.000001", "1003.000001"} {
		insertTestMessage(t, conn, db.Message{TS: ts, ChannelID: channelID, Timestamp: now, ThreadTS: parentTS})
	}

	client := &fakeThreadClient{replies: json.RawMessage(`{
  "ok": true,
  "messages": [
    {"ts":"1000.000001","thread_ts":"1000.000001","reply_count":4},
    {"ts":"1001.000001","thread_ts":"1000.000001"},
    {"ts":"1002.000001","thread_ts":"1000.000001"},
    {"ts":"1003.000001","thread_ts":"1000.000001"},
    {"ts":"1004.000001","thread_ts":"1000.000001","text":"new reply"}
  ],
  "has_more": false
}`)}

	newCount, err := CatchupThreads(conn, client, 7)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("reply requests = %d, want 1", client.calls)
	}
	if newCount != 1 {
		t.Fatalf("new replies = %d, want 1", newCount)
	}
	if exists, err := db.MessageExists(conn, "1004.000001", channelID); err != nil || !exists {
		t.Fatalf("new reply was not stored: exists=%v err=%v", exists, err)
	}
	var replyCount int
	if err := conn.QueryRow("SELECT reply_count FROM messages WHERE ts=? AND channel_id=?", parentTS, channelID).Scan(&replyCount); err != nil {
		t.Fatal(err)
	}
	if replyCount != 4 {
		t.Fatalf("parent reply count = %d, want 4", replyCount)
	}
}
