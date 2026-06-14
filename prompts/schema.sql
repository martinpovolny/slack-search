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
