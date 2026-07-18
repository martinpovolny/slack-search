"""Download Slack bookmarks from subscribed channels."""

import sqlite3
from rich.console import Console

from .slack_client import SlackClient

console = Console()


def download_bookmarks(
    conn: sqlite3.Connection, client: SlackClient
) -> int:
    """Download bookmarks from all subscribed channels. Returns count."""
    channels = conn.execute(
        "SELECT id, name FROM channels WHERE subscribed=1 ORDER BY name"
    ).fetchall()

    total = 0
    for ch in channels:
        try:
            data = client._post("bookmarks.list", channel=ch["id"])
        except Exception as e:
            console.print(f"  [yellow]#{ch['name']}: {e}[/]")
            continue

        bookmarks = data.get("bookmarks", [])
        if not bookmarks:
            continue

        count = 0
        for b in bookmarks:
            bid = b.get("id", "")
            if not bid:
                continue
            conn.execute(
                """INSERT OR REPLACE INTO bookmarks
                   (id, channel_id, title, link, type, emoji, created_at)
                   VALUES (?, ?, ?, ?, ?, ?, ?)""",
                (bid, ch["id"], b.get("title", ""), b.get("link", ""),
                 b.get("type", ""), b.get("emoji", ""),
                 b.get("date_created", 0)),
            )
            count += 1
        conn.commit()

        if count:
            console.print(f"  #{ch['name']}: {count} bookmark(s)")
            total += count

    console.print(f"[green]Done.[/] {total} bookmark(s) across {len(channels)} channel(s).")
    return total
