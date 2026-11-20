package download

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
)

type fakeScanClient struct {
	fakeThreadClient
	history      json.RawMessage
	historyCalls int
	oldest       string
}

func (c *fakeScanClient) ConversationsHistory(params map[string]string) (json.RawMessage, error) {
	c.historyCalls++
	c.oldest = params["oldest"]
	return c.history, nil
}

// Reproduces the lost-thread bug: the parent was archived before its first
// reply, so reply_count=0 hid it from the lookback catch-up.
func TestScanChannelThreadsRecoversThreadOfZeroReplyParent(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const channelID = "C12345678"
	if err := db.UpsertChannel(conn, channelID, "test-channel"); err != nil {
		t.Fatal(err)
	}
	if err := db.SubscribeChannel(conn, channelID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	parentTS := fmt.Sprintf("%d.000100", now-3600)
	replyTS := fmt.Sprintf("%d.000200", now-60)
	insertTestMessage(t, conn, db.Message{TS: parentTS, ChannelID: channelID, Timestamp: float64(now - 3600), Text: "parent"})

	client := &fakeScanClient{
		history: json.RawMessage(fmt.Sprintf(`{"ok":true,"messages":[
			{"ts":%q,"thread_ts":%q,"reply_count":1,"text":"parent"},
			{"ts":"1.0","subtype":"channel_join"}
		],"has_more":false}`, parentTS, parentTS)),
		fakeThreadClient: fakeThreadClient{replies: json.RawMessage(fmt.Sprintf(`{"ok":true,"messages":[
			{"ts":%q,"thread_ts":%q,"reply_count":1,"text":"parent"},
			{"ts":%q,"thread_ts":%q,"text":"the missing reply"}
		],"has_more":false}`, parentTS, parentTS, replyTS, parentTS))},
	}

	result, err := ScanChannelThreads(conn, client, channelID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ScanResult{Days: 7, HistoryMessages: 1, NewMessages: 0, NewThreadReplies: 1}); result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if client.historyCalls != 1 || client.calls != 1 {
		t.Fatalf("history calls = %d, reply calls = %d; want 1 and 1", client.historyCalls, client.calls)
	}
	if exists, _ := db.MessageExists(conn, replyTS, channelID); !exists {
		t.Fatal("missing reply was not archived")
	}
	var oldest int64
	if _, err := fmt.Sscan(client.oldest, &oldest); err != nil || oldest > now-7*86400+5 || oldest < now-7*86400-5 {
		t.Fatalf("history oldest = %q, want about now-7d", client.oldest)
	}
}

func TestScanWindowDaysUsesLargerOfLookbackAndChannelSetting(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.UpsertChannel(conn, "C1", "one"); err != nil {
		t.Fatal(err)
	}
	if got := ScanWindowDays(conn, "C1", 7); got != 7 {
		t.Fatalf("unset channel window = %d, want 7", got)
	}
	if err := db.SetDeepScanDays(conn, "C1", 30); err != nil {
		t.Fatal(err)
	}
	if got := ScanWindowDays(conn, "C1", 7); got != 30 {
		t.Fatalf("deep-scan window = %d, want 30", got)
	}
	if got := ScanWindowDays(conn, "C1", 0); got != 30 {
		t.Fatalf("deep-scan window without lookback = %d, want 30", got)
	}
	if err := db.SetDeepScanDays(conn, "C1", 366); err == nil {
		t.Fatal("expected error for window above the maximum")
	}
	if err := db.SetDeepScanDays(conn, "C-missing", 7); err == nil {
		t.Fatal("expected error for unknown channel")
	}
}
