# Slack-search skill

This document tells a Claude instance how to answer questions about a Slack archive using the `slack-search` CLI and direct read-only SQLite access.

## Setup

The message database lives at `~/.slack-search/messages.db` by default.  The path can be overridden with `--db <path>` on every CLI command.

All commands must be run from the `slack-search` project directory with `uv run slack-search …`.

---

## Tools available

### 1. `grep` — fast full-text / regexp search

Use this when the question is "find messages that contain X" or "show me what was said about Y".

```
uv run slack-search grep [OPTIONS]

  -F, --string TEXT      Literal string (case-insensitive)
  -E, --regexp PATTERN   Regular expression (case-insensitive)
  -c, --channel CHANNEL  Limit to channel name or ID (repeat for multiple)
  --since DATE           After this date  (e.g. '2024-01-01', '3 weeks ago')
  --until DATE           Before this date
  -p, --person NAME      Sender partial name match
  -n, --limit N          Max results (default 200)
  -P, --pager            Page output with colours preserved
```

Examples:

```bash
# Who mentioned "out of memory" in any channel?
uv run slack-search grep -F "out of memory"

# Errors or warnings in cost channels last two weeks
uv run slack-search grep -E "error|warning" \
  --channel cost-mgmt-dev --channel forum-cost-mgmt \
  --since "2 weeks ago"

# What did Martin say about the budget in Q1 2024?
uv run slack-search grep -F "budget" --person Martin \
  --since 2024-01-01 --until 2024-04-01

# All messages from David in cost-team-chat
uv run slack-search grep -E ".*" --channel cost-team-chat --person David
```

### 2. `search` — raw SQL query

Use this for aggregations, counts, joins, or anything grep cannot express.

```bash
uv run slack-search search "SELECT …"
```

Examples:

```bash
# Top senders in the last 30 days
uv run slack-search search "
  SELECT u.real_name, count(*) AS msgs
  FROM messages m JOIN users u ON m.user_id = u.id
  WHERE m.timestamp > unixepoch('now', '-30 days')
  GROUP BY u.id ORDER BY msgs DESC LIMIT 10"

# Thread activity — most-replied messages
uv run slack-search search "
  SELECT datetime(timestamp,'unixepoch') AS time, username, reply_count, text
  FROM messages
  WHERE thread_ts IS NULL AND reply_count > 0
  ORDER BY reply_count DESC LIMIT 20"
```

### 3. Direct SQLite access (read-only)

For multi-step analysis or when you need to iterate over results programmatically:

```bash
sqlite3 ~/.slack-search/messages.db "SELECT …"
```

Or open the DB in Python:

```python
import sqlite3, os
conn = sqlite3.connect(os.path.expanduser("~/.slack-search/messages.db"))
conn.row_factory = sqlite3.Row
rows = conn.execute("SELECT …").fetchall()
```

Always open read-only when you only need to query:

```python
conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
```

---

## Database schema

<!-- SCHEMA_BEGIN -->
```
Tables and columns available for SQL queries:

messages(ts TEXT, channel_id TEXT, user_id TEXT, username TEXT, text TEXT,
         timestamp REAL, thread_ts TEXT, reply_count INTEGER)
  - ts: Slack message timestamp/id (e.g. '1718000000.123456')
  - timestamp: Unix epoch float (same value as ts, for range comparisons)
  - text: message body

channels(id TEXT, name TEXT)

users(id TEXT, name TEXT, real_name TEXT, display_name TEXT)

files(id TEXT, ts TEXT, channel_id TEXT, name TEXT, mimetype TEXT, url TEXT, local_path TEXT)

Useful joins:
  messages m JOIN users u ON m.user_id = u.id
  messages m JOIN channels c ON m.channel_id = c.id
  messages m JOIN files f ON f.ts = m.ts AND f.channel_id = m.channel_id

datetime(timestamp, 'unixepoch') converts timestamp to a readable string.
```
<!-- SCHEMA_END -->

### Extended schema (for complex queries)

<!-- SCHEMA_DDL_BEGIN -->
```sql
-- Channels in the archive
CREATE TABLE channels (
    id   TEXT PRIMARY KEY,   -- Slack channel ID, e.g. C04476G1F7H
    name TEXT NOT NULL       -- channel name without #, e.g. cost-mgmt-dev
);

-- Slack workspace members
CREATE TABLE users (
    id           TEXT PRIMARY KEY,  -- Slack user ID, e.g. U0330HC0BH9
    name         TEXT,              -- short @handle
    real_name    TEXT,              -- full display name
    display_name TEXT               -- profile display name (may differ from real_name)
);

-- Every message (top-level and thread replies)
CREATE TABLE messages (
    ts          TEXT NOT NULL,      -- Slack timestamp/ID, e.g. '1718000000.123456'
    channel_id  TEXT NOT NULL,
    user_id     TEXT,               -- NULL for bot messages
    username    TEXT,               -- display name at post time (denormalised)
    text        TEXT,               -- message body (may contain <@UXXXX> mentions)
    timestamp   REAL NOT NULL,      -- same value as ts cast to float, for range queries
    thread_ts   TEXT,               -- parent ts; non-NULL means this is a reply
    reply_count INTEGER DEFAULT 0,
    raw_json    TEXT,               -- full Slack payload
    PRIMARY KEY (ts, channel_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id)
);

CREATE INDEX idx_messages_timestamp ON messages(timestamp);
CREATE INDEX idx_messages_channel   ON messages(channel_id);

-- File attachments
CREATE TABLE files (
    id          TEXT PRIMARY KEY,
    ts          TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    name        TEXT,
    mimetype    TEXT,
    url         TEXT,
    local_path  TEXT,
    FOREIGN KEY (ts, channel_id) REFERENCES messages(ts, channel_id)
);

-- Per-channel download progress (internal, rarely useful for queries)
CREATE TABLE download_state (
    channel_id  TEXT PRIMARY KEY,
    latest_ts   TEXT,
    oldest_ts   TEXT
);
```
<!-- SCHEMA_DDL_END -->

---

## SQLite dialect — important gotchas

| Do NOT use | Use instead |
|---|---|
| `ILIKE` | `LIKE` (already case-insensitive for ASCII) |
| `NOW()` | `'now'` string, e.g. `unixepoch('now')` |
| `INTERVAL '7 days'` | modifier: `unixepoch('now', '-7 days')` |
| `EXTRACT(DOW …)` | `strftime('%w', timestamp, 'unixepoch')` |
| `DATE_TRUNC(…)` | `strftime('%Y-%m-%d', timestamp, 'unixepoch')` |
| `datetime(…) >= '2024-…'` | `timestamp >= unixepoch('2024-…')` |

**Finding users by name** — always search all three name columns:

```sql
WHERE (u.name LIKE '%martin%' OR u.real_name LIKE '%Martin%' OR u.display_name LIKE '%Martin%')
```

**Mentions in message text** are encoded as `<@UXXXXXXX>` — use LIKE to find them:

```sql
WHERE text LIKE '%<@U0330HC0BH9>%'
```

**Last N days:**

```sql
WHERE timestamp > unixepoch('now', '-7 days')
```

**"Last <weekday>"** — never use the SQLite `weekday` modifier (it goes forward). Use offset arithmetic:

```sql
-- SQLite weekday numbers: 0=Sun 1=Mon 2=Tue 3=Wed 4=Thu 5=Fri 6=Sat
-- Offset per target: Sun=0 Mon=6 Tue=5 Wed=4 Thu=3 Fri=2 Sat=1
-- Example: last Friday (offset=2)
WHERE date(timestamp, 'unixepoch') =
      date('now', '-' || ((cast(strftime('%w','now') as integer) + 2) % 7) || ' days')
```

---

## Recommended workflow for answering questions

1. **Clarify scope**: is this about specific people, channels, a time range?
2. **Try grep first** for content questions — it's fast and shows highlighted context.
3. **Use SQL** for counts, rankings, time series, or cross-channel aggregates.
4. **Combine**: run a SQL query to get candidate message IDs / timestamps, then read those rows for content.
5. **Mention resolution**: `<@UXXXXXXX>` tokens in message text are user IDs. Look them up in the `users` table with `WHERE id = 'UXXXXXXX'` to get the real name.

### Example: "What did the team discuss about costs last week?"

```bash
# Step 1 – get a sample of messages
uv run slack-search grep -E "cost|budget|spend|cloud" \
  --channel cost-mgmt-dev --since "last week" -n 100

# Step 2 – check who was most active on the topic
uv run slack-search search "
  SELECT u.real_name, count(*) AS msgs
  FROM messages m JOIN users u ON m.user_id = u.id
  WHERE m.timestamp > unixepoch('now', '-7 days')
    AND m.text LIKE '%cost%'
  GROUP BY u.id ORDER BY msgs DESC LIMIT 10"
```

### Example: "Summarise thread activity around incident X"

```bash
# Find the root message
uv run slack-search grep -F "incident X" --since "2024-03-01" -n 10

# Fetch all replies in that thread (thread_ts = the root ts value)
uv run slack-search search "
  SELECT datetime(timestamp,'unixepoch') AS time,
         COALESCE(u.real_name, m.username) AS author,
         m.text
  FROM messages m LEFT JOIN users u ON m.user_id = u.id
  WHERE m.thread_ts = '1709300000.123456'
  ORDER BY m.timestamp"
```

---

## Channels in this archive

Run the following to see what channels are available:

```bash
uv run slack-search search "SELECT name, id FROM channels ORDER BY name"
```
