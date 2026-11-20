package download

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
)

type fakeThreadClient struct {
	replies json.RawMessage
	calls   int
}

func TestMessageTextIncludesAttachmentOnlyAlertContent(t *testing.T) {
	msg := map[string]interface{}{
		"subtype": "bot_message",
		"text":    "",
		"attachments": []interface{}{
			map[string]interface{}{
				"fallback": "[appsres11ue1]Alert: App-cost-upload-lag-not-decreasing-In-hccm [FIRING:1] Kafka lag for cost management uploads exceeded 25",
				"fields": []interface{}{
					map[string]interface{}{"title": "Environment", "value": "stage"},
				},
			},
		},
	}

	text := messageText(msg)
	if text == "" {
		t.Fatal("attachment-only bot message produced no searchable text")
	}
	if want := "App-cost-upload-lag-not-decreasing-In-hccm"; !strings.Contains(text, want) {
		t.Fatalf("message text %q does not contain %q", text, want)
	}
}

func TestMessageTextDoesNotDuplicateEquivalentRichTextBlock(t *testing.T) {
	msg := map[string]interface{}{
		"text": "slack search - participant names in private channels",
		"blocks": []interface{}{
			map[string]interface{}{
				"type": "rich_text",
				"elements": []interface{}{
					map[string]interface{}{
						"type": "rich_text_section",
						"elements": []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "slack search - participant names in private channels",
							},
						},
					},
				},
			},
		},
	}

	if got, want := messageText(msg), "slack search - participant names in private channels"; got != want {
		t.Fatalf("messageText() = %q, want %q", got, want)
	}
}

func TestMessageTextKeepsDistinctRichTextBlock(t *testing.T) {
	msg := map[string]interface{}{
		"text": "fallback",
		"blocks": []interface{}{
			map[string]interface{}{
				"type": "rich_text",
				"elements": []interface{}{
					map[string]interface{}{
						"type": "rich_text_section",
						"elements": []interface{}{
							map[string]interface{}{"type": "text", "text": "visible block detail"},
						},
					},
				},
			},
		},
	}

	if got, want := messageText(msg), "fallback\nvisible block detail"; got != want {
		t.Fatalf("messageText() = %q, want %q", got, want)
	}
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
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

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

// A parent archived before its first reply keeps reply_count=0, so the
// lookback catch-up never re-fetches it. FetchThread must backfill the thread
// from a reply's ts (as given by a permalink) regardless of the cached count.
func TestFetchThreadBackfillsThreadOfStaleParent(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const channelID = "C12345678"
	const parentTS = "1000.000001"
	if err := db.UpsertChannel(conn, channelID, "test-channel"); err != nil {
		t.Fatal(err)
	}
	insertTestMessage(t, conn, db.Message{TS: parentTS, ChannelID: channelID, Timestamp: 1000, Text: "parent", RawJSON: json.RawMessage(`{"ts":"1000.000001"}`)})

	client := &fakeThreadClient{replies: json.RawMessage(`{
  "ok": true,
  "messages": [
    {"ts":"1000.000001","thread_ts":"1000.000001","reply_count":2,"text":"parent"},
    {"ts":"1001.000001","thread_ts":"1000.000001","user":"U1","text":"first"},
    {"ts":"1002.000001","thread_ts":"1000.000001","attachments":[{"text":"attachment only"}]}
  ],
  "has_more": false
}`)}

	result, err := FetchThread(conn, client, channelID, "1002.000001")
	if err != nil {
		t.Fatal(err)
	}
	want := ThreadFetchResult{ChannelID: channelID, ThreadTS: parentTS, ReplyCount: 2, Fetched: 3, NewMessages: 2}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	var replyCount int
	if err := conn.QueryRow("SELECT reply_count FROM messages WHERE ts=? AND channel_id=?", parentTS, channelID).Scan(&replyCount); err != nil {
		t.Fatal(err)
	}
	if replyCount != 2 {
		t.Fatalf("parent reply_count = %d, want 2", replyCount)
	}
	var text string
	if err := conn.QueryRow("SELECT text FROM messages WHERE ts='1002.000001'").Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "attachment only") {
		t.Fatalf("attachment-only reply text = %q", text)
	}
}

func TestFetchThreadSetsMissingParentThreadTS(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	const channelID = "C12345678"
	if err := db.UpsertChannel(conn, channelID, "test-channel"); err != nil {
		t.Fatal(err)
	}
	insertTestMessage(t, conn, db.Message{TS: "1000.000001", ChannelID: channelID, Timestamp: 1000, Text: "parent"})
	client := &fakeThreadClient{replies: json.RawMessage(`{"ok":true,"messages":[{"ts":"1000.000001","thread_ts":"1000.000001","reply_count":0,"text":"parent"}]}`)}
	if _, err := FetchThread(conn, client, channelID, "1000.000001"); err != nil {
		t.Fatal(err)
	}
	var threadTS string
	if err := conn.QueryRow("SELECT COALESCE(thread_ts,'') FROM messages WHERE ts='1000.000001'").Scan(&threadTS); err != nil {
		t.Fatal(err)
	}
	if threadTS != "1000.000001" {
		t.Fatalf("parent thread_ts = %q, want 1000.000001", threadTS)
	}
}
