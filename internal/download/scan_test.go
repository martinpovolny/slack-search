package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
)

const scanChannel = "C12345678"

// fakeScanClient serves one history page and per-thread replies.
type fakeScanClient struct {
	history      []map[string]interface{}
	replies      map[string][]map[string]interface{}
	historyCalls int
	replyCalls   []string
	oldest       string
}

func (c *fakeScanClient) ConversationsHistory(params map[string]string) (json.RawMessage, error) {
	c.historyCalls++
	c.oldest = params["oldest"]
	return json.Marshal(map[string]interface{}{"ok": true, "messages": c.history, "has_more": false})
}

func (c *fakeScanClient) ConversationsReplies(params map[string]string) (json.RawMessage, error) {
	c.replyCalls = append(c.replyCalls, params["ts"])
	return json.Marshal(map[string]interface{}{"ok": true, "messages": c.replies[params["ts"]], "has_more": false})
}

func (c *fakeScanClient) UsersInfo(string) (json.RawMessage, error) {
	return json.RawMessage(`{"ok": true}`), nil
}

func openScanDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.UpsertChannel(conn, scanChannel, "test-channel"); err != nil {
		t.Fatal(err)
	}
	if err := db.SubscribeChannel(conn, scanChannel); err != nil {
		t.Fatal(err)
	}
	return conn
}

// ago returns a Slack ts for a moment the given duration ago.
func ago(d time.Duration, micro int) string {
	return fmt.Sprintf("%d.%06d", time.Now().Add(-d).Unix(), micro)
}

func archive(t *testing.T, conn *sql.DB, ts, threadTS string, replyCount int) {
	t.Helper()
	var tsFloat float64
	if _, err := fmt.Sscan(ts, &tsFloat); err != nil {
		t.Fatal(err)
	}
	insertTestMessage(t, conn, db.Message{TS: ts, ChannelID: scanChannel, Timestamp: tsFloat, ThreadTS: threadTS, ReplyCount: replyCount, Text: "m " + ts})
}

func parentMsg(ts string, replies []string) map[string]interface{} {
	m := map[string]interface{}{"ts": ts, "text": "parent " + ts}
	if len(replies) > 0 {
		m["thread_ts"] = ts
		m["reply_count"] = len(replies)
		m["latest_reply"] = replies[len(replies)-1]
	}
	return m
}

func thread(parentTS string, replies []string) []map[string]interface{} {
	msgs := []map[string]interface{}{parentMsg(parentTS, replies)}
	for _, r := range replies {
		msgs = append(msgs, map[string]interface{}{"ts": r, "thread_ts": parentTS, "text": "reply " + r})
	}
	return msgs
}

// Reproduces the lost-thread bug: the parent was archived before its first
// reply, so its archived reply_count is 0. The normal tier must notice that
// Slack reports a reply the archive lacks.
func TestScanNormalTierRecoversThreadOfZeroReplyParent(t *testing.T) {
	conn := openScanDB(t)
	parent, reply := ago(2*24*time.Hour, 100), ago(time.Hour, 200)
	archive(t, conn, parent, "", 0)

	client := &fakeScanClient{
		history: []map[string]interface{}{parentMsg(parent, []string{reply}), {"ts": "1.000000", "subtype": "channel_join"}},
		replies: map[string][]map[string]interface{}{parent: thread(parent, []string{reply})},
	}
	result, err := ScanChannelThreads(conn, client, scanChannel, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := ScanResult{LookbackDays: 7, HistoryMessages: 1, ThreadsFetched: 1, NewThreadReplies: 1}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if exists, _ := db.MessageExists(conn, reply, scanChannel); !exists {
		t.Fatal("missing reply was not archived")
	}
	var oldest int64
	if _, err := fmt.Sscan(client.oldest, &oldest); err != nil || abs(oldest-(time.Now().Unix()-7*86400)) > 5 {
		t.Fatalf("history oldest = %q, want about now-7d", client.oldest)
	}
}

func TestScanNormalTierSkipsUpToDateThreadAndFetchesNewerLatestReply(t *testing.T) {
	conn := openScanDB(t)
	upToDate, upReply := ago(3*24*time.Hour, 100), ago(2*24*time.Hour, 200)
	// Same reply count as archived, but a newer latest reply (one reply was
	// deleted on Slack and another added).
	changed, oldReply, newReply := ago(4*24*time.Hour, 300), ago(3*24*time.Hour, 400), ago(time.Hour, 500)
	archive(t, conn, upToDate, upToDate, 1)
	archive(t, conn, upReply, upToDate, 0)
	archive(t, conn, changed, changed, 1)
	archive(t, conn, oldReply, changed, 0)

	client := &fakeScanClient{
		history: []map[string]interface{}{parentMsg(upToDate, []string{upReply}), parentMsg(changed, []string{newReply})},
		replies: map[string][]map[string]interface{}{changed: thread(changed, []string{newReply})},
	}
	result, err := ScanChannelThreads(conn, client, scanChannel, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.replyCalls, []string{changed}) {
		t.Fatalf("fetched threads = %v, want only %s", client.replyCalls, changed)
	}
	if result.NewThreadReplies != 1 {
		t.Fatalf("new replies = %d, want 1", result.NewThreadReplies)
	}
}

func TestScanDeepTierRefetchesRecentThreadsOnly(t *testing.T) {
	conn := openScanDB(t)
	recent, recentReply := ago(12*time.Hour, 100), ago(6*time.Hour, 200)
	older, olderReply := ago(3*24*time.Hour, 300), ago(2*24*time.Hour, 400)
	for _, p := range [][2]string{{recent, recentReply}, {older, olderReply}} {
		archive(t, conn, p[0], p[0], 1)
		archive(t, conn, p[1], p[0], 0)
	}
	broadcast := map[string]interface{}{"ts": ago(time.Hour, 500), "thread_ts": older, "subtype": "thread_broadcast", "reply_count": 1, "text": "broadcast"}

	client := &fakeScanClient{
		history: []map[string]interface{}{parentMsg(recent, []string{recentReply}), parentMsg(older, []string{olderReply}), broadcast},
		replies: map[string][]map[string]interface{}{recent: thread(recent, []string{recentReply})},
	}
	result, err := ScanChannelThreads(conn, client, scanChannel, 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.replyCalls, []string{recent}) {
		t.Fatalf("fetched threads = %v, want only the deep-window thread %s", client.replyCalls, recent)
	}
	if result.ThreadsFetched != 1 || result.DeepScanDays != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestScanDeepWindowLongerThanLookbackWidensHistory(t *testing.T) {
	conn := openScanDB(t)
	client := &fakeScanClient{}
	if _, err := ScanChannelThreads(conn, client, scanChannel, 2, 10); err != nil {
		t.Fatal(err)
	}
	var oldest int64
	if _, err := fmt.Sscan(client.oldest, &oldest); err != nil || abs(oldest-(time.Now().Unix()-10*86400)) > 5 {
		t.Fatalf("history oldest = %q, want about now-10d", client.oldest)
	}
}

func TestScanSubscribedChannelsSkipsChannelsWithoutWindow(t *testing.T) {
	conn := openScanDB(t)
	client := &fakeScanClient{}
	if _, err := ScanSubscribedChannels(conn, client, 0); err != nil {
		t.Fatal(err)
	}
	if client.historyCalls != 0 {
		t.Fatalf("history calls = %d, want 0 without lookback or deep scan", client.historyCalls)
	}
	if err := db.SetDeepScanDays(conn, scanChannel, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSubscribedChannels(conn, client, 0); err != nil {
		t.Fatal(err)
	}
	if client.historyCalls != 1 {
		t.Fatalf("history calls = %d, want 1 for a deep-scan channel", client.historyCalls)
	}
}

func TestSetDeepScanDaysValidates(t *testing.T) {
	conn := openScanDB(t)
	if err := db.SetDeepScanDays(conn, scanChannel, 30); err != nil {
		t.Fatal(err)
	}
	if days, err := db.DeepScanDays(conn, scanChannel); err != nil || days != 30 {
		t.Fatalf("deep scan days = %d, %v; want 30", days, err)
	}
	if err := db.SetDeepScanDays(conn, scanChannel, db.MaxDeepScanDays+1); err == nil {
		t.Fatal("expected error for window above the maximum")
	}
	if err := db.SetDeepScanDays(conn, "C-missing", 7); err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// A reply missing from the middle of a thread leaves latest_reply unchanged;
// only the reply count reveals the gap.
func TestScanNormalTierFetchesThreadWithMissingMiddleReply(t *testing.T) {
	conn := openScanDB(t)
	parent := ago(3*24*time.Hour, 100)
	r1, r2, r3 := ago(2*24*time.Hour, 200), ago(24*time.Hour, 300), ago(time.Hour, 400)
	archive(t, conn, parent, parent, 3)
	archive(t, conn, r1, parent, 0)
	archive(t, conn, r3, parent, 0)

	client := &fakeScanClient{
		history: []map[string]interface{}{parentMsg(parent, []string{r1, r2, r3})},
		replies: map[string][]map[string]interface{}{parent: thread(parent, []string{r1, r2, r3})},
	}
	result, err := ScanChannelThreads(conn, client, scanChannel, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.replyCalls, []string{parent}) || result.NewThreadReplies != 1 {
		t.Fatalf("fetched = %v, new replies = %d; want the gapped thread and 1 new reply", client.replyCalls, result.NewThreadReplies)
	}
}
