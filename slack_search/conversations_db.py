"""Conversation history store — separate DB so it never locks the message archive."""

import sqlite3
import time
import uuid
from pathlib import Path
from typing import Optional

CURRENT_USER = "martin"  # single-user for now

SCHEMA = """
CREATE TABLE IF NOT EXISTS conversations (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT 'New conversation',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_user ON conversations(user_id, updated_at);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    sql             TEXT,
    created_at      REAL NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_msg_conv ON messages(conversation_id, created_at);
"""


def open_conversations_db(path: Path) -> sqlite3.Connection:
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(path, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.executescript(SCHEMA)
    conn.commit()
    return conn


def list_conversations(conn: sqlite3.Connection, user_id: str = CURRENT_USER) -> list[dict]:
    rows = conn.execute(
        "SELECT id, title, updated_at FROM conversations WHERE user_id=? ORDER BY updated_at DESC",
        (user_id,),
    ).fetchall()
    return [dict(r) for r in rows]


def create_conversation(conn: sqlite3.Connection, user_id: str = CURRENT_USER) -> str:
    cid = str(uuid.uuid4())
    now = time.time()
    conn.execute(
        "INSERT INTO conversations(id, user_id, title, created_at, updated_at) VALUES (?,?,?,?,?)",
        (cid, user_id, "New conversation", now, now),
    )
    conn.commit()
    return cid


def rename_conversation(conn: sqlite3.Connection, cid: str, title: str) -> None:
    conn.execute("UPDATE conversations SET title=? WHERE id=?", (title, cid))
    conn.commit()


def touch_conversation(conn: sqlite3.Connection, cid: str) -> None:
    conn.execute("UPDATE conversations SET updated_at=? WHERE id=?", (time.time(), cid))
    conn.commit()


def delete_conversation(conn: sqlite3.Connection, cid: str) -> None:
    conn.execute("DELETE FROM conversations WHERE id=?", (cid,))
    conn.commit()


def load_messages(conn: sqlite3.Connection, cid: str) -> list[dict]:
    rows = conn.execute(
        "SELECT role, content, sql FROM messages WHERE conversation_id=? ORDER BY created_at",
        (cid,),
    ).fetchall()
    return [
        {k: v for k, v in dict(r).items() if v is not None}
        for r in rows
    ]


def append_message(
    conn: sqlite3.Connection,
    cid: str,
    role: str,
    content: str,
    sql: Optional[str] = None,
) -> None:
    conn.execute(
        "INSERT INTO messages(conversation_id, role, content, sql, created_at) VALUES (?,?,?,?,?)",
        (cid, role, content, sql, time.time()),
    )
    touch_conversation(conn, cid)
    conn.commit()


def auto_title(first_user_message: str, max_len: int = 60) -> str:
    """Derive a short title from the first user message."""
    t = first_user_message.strip().splitlines()[0]
    return t if len(t) <= max_len else t[:max_len - 1] + "…"
