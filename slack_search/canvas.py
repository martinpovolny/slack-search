"""Download and extract Slack Canvas content via the internal Quip protocol."""

import base64
import re
import sqlite3
import time
from typing import Optional

import requests
from rich.console import Console

from .slack_client import SlackClient

console = Console()


def _encode_varint(value: int) -> bytes:
    result = bytearray()
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value)
    return bytes(result)


def _encode_string_field(field_num: int, s: str) -> bytes:
    tag = (field_num << 3) | 2
    data = s.encode("ascii")
    return _encode_varint(tag) + _encode_varint(len(data)) + data


def _encode_varint_field(field_num: int, value: int) -> bytes:
    tag = (field_num << 3) | 0
    return _encode_varint(tag) + _encode_varint(value)


def _build_request_binary(quip_id: str) -> str:
    """Construct the protobuf request_binary for canvas/-/load-data/editor/1."""
    sub = _encode_varint_field(1, 1) + _encode_string_field(2, quip_id)
    field3_tag = _encode_varint((3 << 3) | 2)
    field3 = field3_tag + _encode_varint(len(sub)) + sub
    msg = (
        _encode_string_field(1, quip_id)
        + field3
        + _encode_string_field(5, "editor")
        + _encode_varint_field(6, 1)
    )
    return base64.b64encode(msg).decode()


def _extract_text(raw: bytes) -> tuple[str, str]:
    """Extract readable text and HTML content from protobuf response.

    Returns (plain_text, html_content).
    """
    strings = re.findall(rb"[\x20-\x7e]{15,}", raw)

    lines = []
    html_parts = []
    for s in strings:
        text = s.decode("ascii", errors="replace")
        # Skip structural/internal data
        if re.search(r"temp:", text[:20]):
            continue
        if re.match(r"^(aal|aTY|IfB|dbB|LGU|cSF|GU9|ke8)", text):
            continue
        if re.match(r"^[0-9a-f]{20,}$", text):
            continue
        if re.match(r"^[A-Za-z0-9/+=]{15,}$", text) and " " not in text:
            continue
        if re.match(r"^su:|^zzzzzz-|^Section/", text):
            continue
        if re.match(r"^[0-9a-f]{8,}-\d+$", text):
            continue
        if re.match(r"^\([0-9a-f]{30,}$", text):
            continue
        if re.match(r"^E\d{9,}", text) and " " not in text:
            continue
        if re.match(r"^[A-Z_]{10,}$", text):
            continue
        # Clean trailing protobuf markers
        text = re.sub(r"jL$", "", text).strip()
        if not text or len(text) < 10:
            continue
        # Store HTML version
        html_parts.append(text)
        # Strip HTML for plain text
        plain = re.sub(r"<[^>]+>", "", text).strip()
        # Strip leading protobuf length bytes
        plain = re.sub(r"^[A-Z^>]\s*(?=[A-Z])", "", plain)
        # Strip leading non-alpha chars
        plain = re.sub(r"^[^a-zA-Z0-9(\"]+", "", plain)
        if plain and len(plain) > 8:
            lines.append(plain)

    return "\n".join(lines), "\n".join(html_parts)


def discover_canvases(
    client: SlackClient, conn: sqlite3.Connection
) -> list[dict]:
    """Find canvases attached to subscribed channels.

    Returns list of {channel_id, channel_name, file_id, quip_id, is_empty}.
    """
    channels = conn.execute(
        "SELECT id, name FROM channels WHERE subscribed=1 ORDER BY name"
    ).fetchall()

    results = []
    for ch in channels:
        try:
            data = client.conversations_info(channel=ch["id"])
        except Exception:
            continue
        props = data.get("channel", {}).get("properties", {})

        # Channel canvas
        canvas = props.get("canvas", {})
        if canvas.get("file_id") and not canvas.get("is_empty"):
            results.append({
                "channel_id": ch["id"],
                "channel_name": ch["name"],
                "file_id": canvas["file_id"],
                "quip_id": canvas.get("quip_thread_id", ""),
                "source": "channel_canvas",
            })

        # Canvas tabs (check both "tabs" and "tabz" — Slack uses both)
        all_tabs = props.get("tabs", []) + props.get("tabz", [])
        for tab in all_tabs:
            if tab.get("type") == "canvas" and tab.get("data", {}).get("file_id"):
                fid = tab["data"]["file_id"]
                if fid not in [r["file_id"] for r in results]:
                    results.append({
                        "channel_id": ch["id"],
                        "channel_name": ch["name"],
                        "file_id": fid,
                        "quip_id": "",
                        "source": f"tab:{tab.get('label', '')}",
                    })

    # Resolve quip IDs for any that are missing
    for r in results:
        if not r["quip_id"]:
            try:
                lookup = client._post("quip.lookupThreadIds", file_ids=r["file_id"])
                r["quip_id"] = lookup.get("lookup", {}).get(r["file_id"], "")
            except Exception:
                pass

    return results


def fetch_canvas(
    session: requests.Session,
    workspace: str,
    token: str,
    quip_id: str,
) -> Optional[tuple[str, str]]:
    """Fetch canvas content. Returns (plain_text, html_content) or None."""
    request_binary = _build_request_binary(quip_id)
    try:
        resp = session.post(
            f"https://{workspace}/canvas/-/load-data/editor/1",
            data={
                "token": token,
                "request_binary": request_binary,
                "_resource_bundle": "collab_controller",
                "_version": "10",
            },
            timeout=30,
        )
        if resp.ok and len(resp.content) > 100:
            return _extract_text(resp.content)
    except Exception as e:
        console.print(f"  [yellow]Canvas fetch error: {e}[/]")
    return None


def download_canvases(
    conn: sqlite3.Connection, client: SlackClient, workspace: str
) -> int:
    """Discover and download all canvases from subscribed channels. Returns count."""
    canvases = discover_canvases(client, conn)
    if not canvases:
        console.print("[dim]No canvases found in subscribed channels.[/]")
        return 0

    console.print(f"[cyan]Found {len(canvases)} canvas(es) to download…[/]")

    session = client.session
    count = 0
    for c in canvases:
        if not c["quip_id"]:
            console.print(f"  [yellow]#{c['channel_name']}: no quip ID, skipping[/]")
            continue

        console.print(f"  #{c['channel_name']} ({c['file_id']})…", end=" ")
        result = fetch_canvas(session, workspace, client.token, c["quip_id"])
        if result is None:
            console.print("[red]failed[/]")
            continue

        plain_text, html_content = result
        # Extract title: look for a heading-like line (skip short/ID-like lines)
        title = ""
        for line in plain_text.split("\n"):
            line = line.strip()
            if len(line) < 5 or line.startswith("(") or re.match(r"^[A-Za-z0-9]{11,}$", line):
                continue
            title = line[:100]
            break

        conn.execute(
            """INSERT OR REPLACE INTO canvases
               (file_id, channel_id, quip_id, title, content_text, content_html, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (c["file_id"], c["channel_id"], c["quip_id"], title,
             plain_text, html_content, time.time()),
        )
        conn.commit()
        count += 1
        console.print(f"[green]✓[/] {len(plain_text)} chars, title: {title[:50]}")

    console.print(f"[green]Done.[/] {count} canvas(es) downloaded.")
    return count
