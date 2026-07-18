"""Full-text / regexp grep over the message archive."""
from __future__ import annotations

import re
import sqlite3
from typing import Optional


def _register_regexp(conn: sqlite3.Connection) -> None:
    """Add a case-insensitive REGEXP function to the connection."""
    conn.create_function(
        "regexp", 2,
        lambda pattern, text: bool(re.search(pattern, text or "", re.IGNORECASE)),
    )


def _resolve_channel_ids(conn: sqlite3.Connection, names_or_ids: tuple[str, ...]) -> list[str]:
    """Return channel IDs for a list of channel names or IDs. Raises ValueError if any unknown."""
    ids = []
    for raw in names_or_ids:
        name = raw.lstrip("#")
        row = conn.execute(
            "SELECT id FROM channels WHERE id=? OR name=?", (name, name)
        ).fetchone()
        if not row:
            raise ValueError(f"Channel '{raw}' not found in database. Run 'download' first.")
        ids.append(row[0])
    return ids


def grep_messages(
    conn: sqlite3.Connection,
    *,
    fixed_string: Optional[str] = None,
    pattern: Optional[str] = None,
    channels: tuple[str, ...] = (),
    since: Optional[str] = None,
    until: Optional[str] = None,
    person: Optional[str] = None,
    limit: int = 200,
) -> list[dict]:
    """Search messages and return a list of result dicts.

    Exactly one of fixed_string or pattern must be provided.
    """
    if not fixed_string and not pattern:
        raise ValueError("Provide --string or --regexp.")
    if fixed_string and pattern:
        raise ValueError("--string and --regexp are mutually exclusive.")

    if pattern:
        _register_regexp(conn)

    where: list[str] = []
    params: list = []

    if fixed_string:
        where.append("m.text LIKE ?")
        params.append(f"%{fixed_string}%")
    else:
        where.append("m.text REGEXP ?")
        params.append(pattern)

    if channels:
        channel_ids = _resolve_channel_ids(conn, channels)
        placeholders = ",".join("?" * len(channel_ids))
        where.append(f"m.channel_id IN ({placeholders})")
        params.extend(channel_ids)

    if since:
        where.append("m.timestamp >= ?")
        params.append(float(since))

    if until:
        where.append("m.timestamp <= ?")
        params.append(float(until))

    if person:
        like = f"%{person}%"
        where.append(
            "(u.name LIKE ? OR u.real_name LIKE ? OR u.display_name LIKE ? OR m.username LIKE ?)"
        )
        params.extend([like, like, like, like])

    where_sql = ("WHERE " + " AND ".join(where)) if where else ""
    sql = f"""
        SELECT
            datetime(m.timestamp, 'unixepoch') AS time,
            c.name                              AS channel,
            COALESCE(u.real_name, u.display_name, m.username, m.user_id, '(bot)') AS author,
            m.text,
            m.ts,
            m.thread_ts
        FROM messages m
        LEFT JOIN users    u ON m.user_id    = u.id
        LEFT JOIN channels c ON m.channel_id = c.id
        {where_sql}
        ORDER BY m.timestamp ASC
        LIMIT ?
    """
    params.append(limit)
    rows = conn.execute(sql, params).fetchall()
    return [dict(r) for r in rows]
