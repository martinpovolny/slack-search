"""Slack text formatting utilities shared between CLI, web UI, and AI pipeline."""
from __future__ import annotations

import re
import sqlite3

_MENTION_RE = re.compile(r"<@([A-Z0-9]+)(?:\|[^>]*)?>")


def extract_uids(texts: list[str]) -> set[str]:
    """Return all Slack user IDs found in a list of message texts."""
    uids: set[str] = set()
    for text in texts:
        if text:
            uids.update(m.group(1) for m in _MENTION_RE.finditer(text))
    return uids


def build_user_map(conn: sqlite3.Connection, uids: set[str]) -> dict[str, str]:
    """Return {user_id: display_name} for the given set of IDs."""
    if not uids:
        return {}
    placeholders = ",".join("?" * len(uids))
    rows = conn.execute(
        f"SELECT id, COALESCE(real_name, display_name, name, id) AS name "
        f"FROM users WHERE id IN ({placeholders})",
        list(uids),
    ).fetchall()
    return {r[0]: r[1] for r in rows}


def resolve_mentions(text: str, user_map: dict[str, str]) -> str:
    """Replace <@UXXXXXXX> tokens with @Real Name using the provided map."""
    if not text:
        return text
    return _MENTION_RE.sub(
        lambda m: f"@{user_map.get(m.group(1), m.group(1))}",
        text,
    )


def resolve_mentions_in_texts(
    texts: list[str], conn: sqlite3.Connection
) -> list[str]:
    """Convenience: resolve mentions for a list of texts in one DB round-trip."""
    uids = extract_uids(texts)
    user_map = build_user_map(conn, uids)
    return [resolve_mentions(t, user_map) for t in texts]
