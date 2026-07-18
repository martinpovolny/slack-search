"""Slack live search — calls search.messages and caches results in the local DB."""
from __future__ import annotations

import re
import sqlite3
from datetime import datetime, timezone

from .slack_client import SlackClient
from .database import upsert_channel, message_exists, insert_message


def extract_highlight_term(query: str) -> str:
    """Return the best plain-text term to highlight from a Slack search query.

    Strips Slack search operators (in:, from:, before:, after:, has:, is:, -token)
    and prefers a quoted exact phrase when present.
    """
    m = re.search(r'"([^"]+)"', query)
    if m:
        return m.group(1)
    cleaned = re.sub(r'\b(?:in|from|before|after|during|to|has|is):\S+', '', query)
    cleaned = re.sub(r'-\S+', '', cleaned)
    return cleaned.strip()


def run_slack_search(
    conn: sqlite3.Connection,
    client: SlackClient,
    query: str,
    limit: int = 50,
) -> list[dict]:
    """Call Slack's search.messages API, cache any new messages, and return normalised rows.

    Caching uses INSERT OR IGNORE into the shared messages table — it never touches
    download_state, so incremental channel downloads are unaffected.
    """
    page_count = min(limit, 100)
    data = client.search_messages(query=query, count=page_count, sort="timestamp", sort_dir="desc")
    matches = data.get("messages", {}).get("matches", [])[:limit]

    results: list[dict] = []
    for match in matches:
        channel_obj = match.get("channel", {})
        channel_id = channel_obj.get("id", "")
        channel_name = channel_obj.get("name", channel_id)
        ts = match.get("ts", "")
        user_id = match.get("user", "")
        username = match.get("username", "")
        text = match.get("text", "")
        permalink = match.get("permalink", "")
        thread_ts = match.get("thread_ts")

        if not (ts and channel_id):
            continue

        # Resolve DM channel names — Slack returns user ID or channel ID as name
        if channel_id.startswith("D") and (channel_name.startswith("U") or channel_name == channel_id):
            if channel_name.startswith("U"):
                row = conn.execute(
                    "SELECT real_name FROM users WHERE id=?", (channel_name,)
                ).fetchone()
                if row and row["real_name"]:
                    channel_name = f"DM: {row['real_name']}"

        upsert_channel(conn, channel_id, channel_name)

        if not message_exists(conn, ts, channel_id):
            if user_id:
                conn.execute(
                    "INSERT OR IGNORE INTO users(id, name) VALUES (?, ?)",
                    (user_id, username),
                )
            flat = {
                "ts": ts,
                "user": user_id,
                "username": username,
                "text": text,
                "thread_ts": thread_ts,
                "reply_count": match.get("reply_count", 0),
            }
            insert_message(conn, flat, channel_id)
            conn.commit()

        # Resolve richer author name from DB (may have been populated by a prior download)
        author = username
        if user_id:
            row = conn.execute(
                "SELECT COALESCE(real_name, display_name, name, id) AS n FROM users WHERE id=?",
                (user_id,),
            ).fetchone()
            if row:
                author = row["n"] or username

        try:
            time_str = datetime.fromtimestamp(float(ts), tz=timezone.utc).strftime("%Y-%m-%d %H:%M")
        except (ValueError, TypeError):
            time_str = ts

        is_reply = bool(thread_ts and thread_ts != ts)
        results.append({
            "time": time_str,
            "channel": channel_name,
            "author": author,
            "text": ("↳ " if is_reply else "") + text,
            "permalink": permalink,
            "ts": ts,
            "channel_id": channel_id,
        })

    return results
