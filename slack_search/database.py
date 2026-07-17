import sqlite3
import json
from pathlib import Path
from typing import Optional


SCHEMA = """
CREATE TABLE IF NOT EXISTS channels (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    subscribed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    name         TEXT,
    real_name    TEXT,
    display_name TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    ts          TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    user_id     TEXT,
    username    TEXT,
    text        TEXT,
    timestamp   REAL NOT NULL,
    thread_ts   TEXT,
    reply_count INTEGER DEFAULT 0,
    raw_json    TEXT,
    PRIMARY KEY (ts, channel_id),
    FOREIGN KEY (channel_id) REFERENCES channels(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_channel   ON messages(channel_id);

CREATE TABLE IF NOT EXISTS files (
    id          TEXT PRIMARY KEY,
    ts          TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    name        TEXT,
    mimetype    TEXT,
    url         TEXT,
    local_path  TEXT,
    FOREIGN KEY (ts, channel_id) REFERENCES messages(ts, channel_id)
);

CREATE TABLE IF NOT EXISTS download_state (
    channel_id      TEXT PRIMARY KEY,
    latest_ts       TEXT,
    oldest_ts       TEXT
);

CREATE TABLE IF NOT EXISTS canvases (
    file_id       TEXT PRIMARY KEY,
    channel_id    TEXT NOT NULL,
    quip_id       TEXT,
    title         TEXT,
    content_text  TEXT,
    content_html  TEXT,
    updated_at    REAL,
    FOREIGN KEY (channel_id) REFERENCES channels(id)
);
"""


def _migrate(conn: sqlite3.Connection) -> None:
    """Apply incremental schema migrations that CREATE TABLE IF NOT EXISTS cannot handle."""
    cols = {row[1] for row in conn.execute("PRAGMA table_info(channels)")}
    if "subscribed" not in cols:
        conn.execute("ALTER TABLE channels ADD COLUMN subscribed INTEGER NOT NULL DEFAULT 0")
        # Auto-subscribe channels that have a download_state entry.
        conn.execute(
            "UPDATE channels SET subscribed=1 WHERE id IN (SELECT channel_id FROM download_state)"
        )
        conn.commit()


def open_db(path: Path) -> sqlite3.Connection:
    conn = sqlite3.connect(path, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.executescript(SCHEMA)
    _migrate(conn)
    conn.commit()
    return conn


def open_db_readonly(path: Path) -> sqlite3.Connection:
    """Open an existing database read-only, safe for use across threads (e.g. Streamlit)."""
    uri = path.absolute().as_uri() + "?mode=ro"
    conn = sqlite3.connect(uri, uri=True, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    return conn


def upsert_channel(conn: sqlite3.Connection, channel_id: str, name: str) -> None:
    # ON CONFLICT DO UPDATE preserves the existing subscribed value.
    conn.execute(
        "INSERT INTO channels(id, name) VALUES (?, ?)"
        " ON CONFLICT(id) DO UPDATE SET name=excluded.name",
        (channel_id, name),
    )


def subscribe_channel(conn: sqlite3.Connection, channel_id: str) -> None:
    conn.execute("UPDATE channels SET subscribed=1 WHERE id=?", (channel_id,))


def lookup_channel_id(conn: sqlite3.Connection, name: str) -> Optional[str]:
    """Return the stored channel ID for a given name, or None if not cached."""
    row = conn.execute(
        "SELECT id FROM channels WHERE name = ?", (name.lstrip("#"),)
    ).fetchone()
    return row["id"] if row else None


def upsert_user(conn: sqlite3.Connection, user: dict) -> None:
    profile = user.get("profile", {})
    conn.execute(
        """
        INSERT OR REPLACE INTO users(id, name, real_name, display_name)
        VALUES (?, ?, ?, ?)
        """,
        (
            user["id"],
            user.get("name"),
            profile.get("real_name") or user.get("real_name"),
            profile.get("display_name"),
        ),
    )


def message_exists(conn: sqlite3.Connection, ts: str, channel_id: str) -> bool:
    row = conn.execute(
        "SELECT 1 FROM messages WHERE ts=? AND channel_id=?", (ts, channel_id)
    ).fetchone()
    return row is not None


def insert_message(conn: sqlite3.Connection, msg: dict, channel_id: str) -> None:
    conn.execute(
        """
        INSERT OR IGNORE INTO messages
            (ts, channel_id, user_id, username, text, timestamp, thread_ts, reply_count, raw_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            msg["ts"],
            channel_id,
            msg.get("user"),
            msg.get("username"),
            msg.get("text", ""),
            float(msg["ts"]),
            msg.get("thread_ts"),
            msg.get("reply_count", 0),
            json.dumps(msg),
        ),
    )


def insert_file(
    conn: sqlite3.Connection,
    file: dict,
    ts: str,
    channel_id: str,
    local_path: Optional[str] = None,
) -> None:
    conn.execute(
        """
        INSERT OR IGNORE INTO files(id, ts, channel_id, name, mimetype, url, local_path)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        (
            file["id"],
            ts,
            channel_id,
            file.get("name"),
            file.get("mimetype"),
            file.get("url_private_download") or file.get("url_private"),
            local_path,
        ),
    )


def get_download_state(conn: sqlite3.Connection, channel_id: str) -> dict:
    row = conn.execute(
        "SELECT latest_ts, oldest_ts FROM download_state WHERE channel_id=?",
        (channel_id,),
    ).fetchone()
    return dict(row) if row else {}


def set_download_state(
    conn: sqlite3.Connection,
    channel_id: str,
    latest_ts: Optional[str] = None,
    oldest_ts: Optional[str] = None,
) -> None:
    existing = get_download_state(conn, channel_id)
    new_latest = latest_ts or existing.get("latest_ts")
    new_oldest = oldest_ts or existing.get("oldest_ts")
    conn.execute(
        """
        INSERT OR REPLACE INTO download_state(channel_id, latest_ts, oldest_ts)
        VALUES (?, ?, ?)
        """,
        (channel_id, new_latest, new_oldest),
    )


def get_ts_gaps(conn: sqlite3.Connection, channel_id: str) -> list[tuple[str, str]]:
    """Return (older_ts, newer_ts) pairs of consecutive messages more than 5min apart."""
    rows = conn.execute(
        "SELECT ts FROM messages WHERE channel_id=? ORDER BY timestamp",
        (channel_id,),
    ).fetchall()
    if len(rows) < 2:
        return []
    gaps = []
    for a, b in zip(rows, rows[1:]):
        if float(b["ts"]) - float(a["ts"]) > 300:
            gaps.append((a["ts"], b["ts"]))
    return gaps
