import sqlite3
import requests
from pathlib import Path
from typing import Optional, Iterator

from rich.console import Console

from .slack_client import SlackClient
from .database import (
    upsert_channel,
    subscribe_channel,
    upsert_user,
    message_exists,
    insert_message,
    insert_file,
    get_download_state,
    set_download_state,
    get_ts_gaps,
    lookup_channel_id,
)

console = Console()


def _resolve_channel(
    client: SlackClient,
    channel_name: str,
    hint_id: Optional[str] = None,
    conn: Optional[sqlite3.Connection] = None,
) -> tuple[str, str]:
    """Return (channel_id, channel_name). Accepts name, #name, or channel ID."""
    stripped = channel_name.lstrip("#")

    if stripped.startswith(("C", "G", "D")) and len(stripped) > 8:
        data = client.conversations_info(channel=stripped)
        ch = data["channel"]
        return ch["id"], ch.get("name") or stripped

    # Check local DB cache first (avoids API calls on repeat runs)
    if conn:
        cached_id = lookup_channel_id(conn, stripped)
        if cached_id:
            return cached_id, stripped

    # Try hint_id first (useful in Enterprise Slack where conversations.list is restricted)
    if hint_id:
        try:
            data = client.conversations_info(channel=hint_id)
            ch = data["channel"]
            ch_name = ch.get("name", "")
            if ch_name == stripped or not stripped:
                return ch["id"], ch_name
        except Exception:
            pass

    # Fallback: search by name via conversations.list
    cursor = None
    while True:
        params: dict = dict(types="public_channel,private_channel,mpim,im", limit=200)
        if cursor:
            params["cursor"] = cursor
        try:
            data = client.conversations_list(**params)
        except RuntimeError as e:
            if "enterprise_is_restricted" in str(e):
                raise ValueError(
                    f"Cannot resolve channel name '{channel_name}' — "
                    "this Enterprise Slack workspace restricts channel listing.\n"
                    "Use the channel ID directly (e.g. --channel C04476G1F7H).\n"
                    "You can find the ID in the URL when you open the channel, "
                    "or from the --curl payload."
                ) from e
            raise
        for ch in data.get("channels", []):
            if ch.get("name") == stripped:
                return ch["id"], ch["name"]
        meta = data.get("response_metadata", {})
        cursor = meta.get("next_cursor")
        if not cursor:
            break
    raise ValueError(f"Channel '{channel_name}' not found.")


def _iter_history(
    client: SlackClient,
    channel_id: str,
    oldest: Optional[str] = None,
    latest: Optional[str] = None,
) -> Iterator[dict]:
    cursor = None
    while True:
        params: dict = dict(channel=channel_id, limit=200, inclusive=False)
        if oldest:
            params["oldest"] = oldest
        if latest:
            params["latest"] = latest
        if cursor:
            params["cursor"] = cursor
        data = client.conversations_history(**params)
        yield from data.get("messages", [])
        if not data.get("has_more"):
            break
        cursor = data["response_metadata"]["next_cursor"]


def _iter_replies(
    client: SlackClient,
    channel_id: str,
    thread_ts: str,
) -> Iterator[dict]:
    """Yield all replies in a thread, skipping the parent at index 0."""
    cursor = None
    first_page = True
    while True:
        params: dict = dict(channel=channel_id, ts=thread_ts, limit=200)
        if cursor:
            params["cursor"] = cursor
        data = client.conversations_replies(**params)
        msgs = data.get("messages", [])
        yield from msgs[1:] if first_page else msgs
        first_page = False
        if not data.get("has_more"):
            break
        cursor = data["response_metadata"]["next_cursor"]


def _download_file(
    url: str,
    session: requests.Session,
    dest_dir: Path,
    filename: str,
) -> Optional[Path]:
    import hashlib
    dest_dir.mkdir(parents=True, exist_ok=True)
    prefix = hashlib.md5(url.encode()).hexdigest()[:8]
    dest = dest_dir / f"{prefix}_{filename}"
    if dest.exists():
        return dest
    try:
        resp = session.get(url, timeout=60, stream=True)
        if resp.ok:
            with dest.open("wb") as fh:
                for chunk in resp.iter_content(65536):
                    fh.write(chunk)
            return dest
        console.print(f"[yellow]  Failed to download {filename}: HTTP {resp.status_code}[/]")
    except requests.RequestException as e:
        console.print(f"[yellow]  Download error for {filename}: {e}[/]")
    return None


def _parse_since(since: Optional[str]) -> Optional[str]:
    if since is None:
        return None
    try:
        float(since)
        return since
    except ValueError:
        pass
    import re
    from datetime import datetime, timedelta, timezone

    # Handle "N unit ago" that dateutil doesn't support
    m = re.fullmatch(
        r'(\d+)\s+(second|minute|hour|day|week|month)s?\s+ago',
        since.strip(), re.IGNORECASE
    )
    if m:
        n, unit = int(m.group(1)), m.group(2).lower()
        delta = {
            'second': timedelta(seconds=n),
            'minute': timedelta(minutes=n),
            'hour':   timedelta(hours=n),
            'day':    timedelta(days=n),
            'week':   timedelta(weeks=n),
            'month':  timedelta(days=n * 30),
        }[unit]
        return str((datetime.now(timezone.utc) - delta).timestamp())

    from dateutil import parser as dateparser
    return str(dateparser.parse(since).timestamp())


def download(
    conn: sqlite3.Connection,
    token: str,
    channel: str,
    since: Optional[str] = None,
    files_dir: Optional[Path] = None,
    check_missing: bool = False,
    fetch_threads: bool = True,
    cookie: Optional[str] = None,
    workspace: Optional[str] = None,
    raw_cookies: Optional[str] = None,
    channel_id_hint: Optional[str] = None,
) -> int:
    """Download messages and thread replies. Returns count of new messages stored."""
    client = SlackClient(token=token, cookie=cookie, workspace=workspace, raw_cookies=raw_cookies)
    channel_id, channel_name = _resolve_channel(
        client, channel, hint_id=channel_id_hint, conn=conn
    )
    upsert_channel(conn, channel_id, channel_name)
    subscribe_channel(conn, channel_id)
    conn.commit()

    state = get_download_state(conn, channel_id)
    seen_users: set[str] = set()
    new_count = 0
    since_ts = _parse_since(since)

    def cache_user(user_id: Optional[str]) -> None:
        if not user_id or user_id in seen_users:
            return
        seen_users.add(user_id)
        try:
            data = client.users_info(user=user_id)
            upsert_user(conn, data["user"])
        except Exception:
            pass

    def store_message(msg: dict) -> tuple[bool, bool]:
        """Store a message. Returns (is_new, has_new_replies)."""
        nonlocal new_count
        ts = msg["ts"]
        api_rc = msg.get("reply_count", 0)
        if message_exists(conn, ts, channel_id):
            if api_rc > 0:
                row = conn.execute(
                    "SELECT reply_count FROM messages WHERE ts=? AND channel_id=?",
                    (ts, channel_id),
                ).fetchone()
                stored_rc = row["reply_count"] if row else 0
                if api_rc > stored_rc:
                    conn.execute(
                        "UPDATE messages SET reply_count=? WHERE ts=? AND channel_id=?",
                        (api_rc, ts, channel_id),
                    )
                    return False, True
            return False, False
        cache_user(msg.get("user"))
        insert_message(conn, msg, channel_id)
        new_count += 1
        for f in msg.get("files", []):
            local_path = None
            if files_dir:
                url = f.get("url_private_download") or f.get("url_private")
                if url:
                    name = f.get("name") or f["id"]
                    dest = _download_file(url, client.session, files_dir / channel_name, name)
                    local_path = str(dest) if dest else None
            insert_file(conn, f, ts, channel_id, local_path)
        return True, False

    def process_batch(msgs: Iterator[dict], track_bounds: bool = True) -> None:
        first_ts = last_ts = None
        threads_to_fetch: list[str] = []

        for msg in msgs:
            if msg.get("subtype") in {"channel_join", "channel_leave"}:
                continue
            if msg.get("subtype") == "bot_message" and not msg.get("text"):
                continue

            ts = msg["ts"]
            if first_ts is None:
                first_ts = ts
            last_ts = ts

            is_new, has_new_replies = store_message(msg)
            if fetch_threads and msg.get("reply_count", 0) > 0 and (is_new or has_new_replies or check_missing):
                threads_to_fetch.append(msg.get("thread_ts") or ts)

            conn.commit()

        for thread_ts in threads_to_fetch:
            console.print(f"  [dim]Fetching thread {thread_ts}…[/]")
            for reply in _iter_replies(client, channel_id, thread_ts):
                store_message(reply)
            conn.commit()

        if track_bounds and first_ts and last_ts:
            set_download_state(conn, channel_id, latest_ts=first_ts, oldest_ts=last_ts)
            conn.commit()

    if check_missing:
        console.print(f"[cyan]Checking for gaps in #{channel_name}…[/]")
        for older_ts, newer_ts in get_ts_gaps(conn, channel_id):
            console.print(f"  Filling gap {older_ts} → {newer_ts}")
            process_batch(
                _iter_history(client, channel_id, oldest=older_ts, latest=newer_ts),
                track_bounds=False,
            )
        if state.get("oldest_ts"):
            console.print("  Fetching before oldest known message…")
            process_batch(
                _iter_history(client, channel_id, oldest=since_ts, latest=state["oldest_ts"]),
                track_bounds=False,
            )
    else:
        if since_ts:
            # Explicit --since always wins: fetch that full window regardless of stored state.
            # Also pull any new messages beyond what we already have.
            stored_latest = state.get("latest_ts")
            if stored_latest and float(stored_latest) > float(since_ts):
                # Two passes: fill history back to since_ts, then fetch anything new.
                oldest_known = state.get("oldest_ts")
                if oldest_known and float(oldest_known) > float(since_ts):
                    console.print(f"[cyan]Backfilling #{channel_name} from {since_ts} to {oldest_known}…[/]")
                    process_batch(
                        _iter_history(client, channel_id, oldest=since_ts, latest=oldest_known),
                        track_bounds=False,
                    )
                console.print(f"[cyan]Fetching new messages in #{channel_name}…[/]")
                process_batch(_iter_history(client, channel_id, oldest=stored_latest))
            else:
                console.print(f"[cyan]Downloading #{channel_name} since {since_ts}…[/]")
                process_batch(_iter_history(client, channel_id, oldest=since_ts))
        else:
            oldest_arg = state.get("latest_ts")
            label = f"new messages since last run" if oldest_arg else "all history"
            console.print(f"[cyan]Downloading #{channel_name} ({label})…[/]")
            process_batch(_iter_history(client, channel_id, oldest=oldest_arg))

    # Enrich any user records in this channel that are missing real_name.
    # This catches users introduced by live-search caching (minimal records)
    # whose messages were already in the DB and therefore skipped store_message.
    incomplete = conn.execute(
        """
        SELECT DISTINCT m.user_id
        FROM messages m
        LEFT JOIN users u ON m.user_id = u.id
        WHERE m.channel_id = ?
          AND m.user_id IS NOT NULL
          AND (u.id IS NULL OR u.real_name IS NULL)
        """,
        (channel_id,),
    ).fetchall()
    if incomplete:
        console.print(f"  [dim]Enriching {len(incomplete)} incomplete user record(s)…[/]")
        for (uid,) in incomplete:
            try:
                data = client.users_info(user=uid)
                upsert_user(conn, data["user"])
            except Exception:
                pass
        conn.commit()

    return new_count


def catchup_threads(
    conn: sqlite3.Connection,
    client: SlackClient,
    lookback_days: int = 7,
) -> int:
    """Re-fetch threads that grew since the last download.

    Scans messages within the lookback window that have a reply_count in the DB
    and re-fetches their replies via conversations.replies. This catches thread
    activity that happened after the initial download — the normal refresh only
    sees new top-level messages.
    """
    import time as _time
    cutoff = _time.time() - (lookback_days * 86400)

    # Find threads within the lookback window that have replies
    rows = conn.execute(
        """
        SELECT m.ts, m.channel_id, m.reply_count, c.name
        FROM messages m
        JOIN channels c ON m.channel_id = c.id
        WHERE c.subscribed = 1
          AND m.timestamp >= ?
          AND m.reply_count > 0
          AND (m.thread_ts IS NULL OR m.thread_ts = m.ts)
        ORDER BY m.timestamp DESC
        """,
        (cutoff,),
    ).fetchall()

    if not rows:
        console.print("[dim]No threads to check in lookback window.[/]")
        return 0

    console.print(f"[cyan]Checking {len(rows)} thread(s) in last {lookback_days} day(s)…[/]")
    new_count = 0

    for row in rows:
        thread_ts, channel_id, stored_rc, channel_name = (
            row["ts"], row["channel_id"], row["reply_count"], row["name"],
        )

        # Count how many replies we actually have stored
        actual = conn.execute(
            "SELECT count(*) FROM messages WHERE thread_ts=? AND channel_id=? AND ts!=?",
            (thread_ts, channel_id, thread_ts),
        ).fetchone()[0]

        if actual >= stored_rc:
            continue

        console.print(f"  [dim]#{channel_name} thread {thread_ts}: {actual}/{stored_rc} replies, fetching…[/]")
        for reply in _iter_replies(client, channel_id, thread_ts):
            ts = reply.get("ts", "")
            if not ts or message_exists(conn, ts, channel_id):
                continue
            user_id = reply.get("user")
            if user_id:
                try:
                    data = client.users_info(user=user_id)
                    upsert_user(conn, data["user"])
                except Exception:
                    pass
            insert_message(conn, reply, channel_id)
            new_count += 1

        # Update the parent's reply_count to match API
        try:
            api_data = client.conversations_replies(channel=channel_id, ts=thread_ts, limit=1)
            parent = api_data.get("messages", [{}])[0]
            api_rc = parent.get("reply_count", stored_rc)
            conn.execute(
                "UPDATE messages SET reply_count=? WHERE ts=? AND channel_id=?",
                (api_rc, thread_ts, channel_id),
            )
        except Exception:
            pass

        conn.commit()

    console.print(f"[green]Thread catchup done.[/] {new_count} new reply(ies) found.")
    return new_count
