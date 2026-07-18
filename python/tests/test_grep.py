"""Tests for slack_search.grep.grep_messages()."""
import sqlite3
import pytest
from slack_search.grep import grep_messages
from slack_search.database import open_db


@pytest.fixture
def conn():
    """In-memory DB with schema and a small set of fixture messages."""
    db = open_db(":memory:")

    db.executemany(
        "INSERT OR IGNORE INTO channels(id, name) VALUES (?, ?)",
        [("C001", "general"), ("C002", "engineering"), ("C003", "random")],
    )
    db.executemany(
        "INSERT OR IGNORE INTO users(id, name, real_name, display_name) VALUES (?, ?, ?, ?)",
        [
            ("U001", "alice", "Alice Smith", "alice"),
            ("U002", "bob",   "Bob Jones",   "bob"),
            ("U003", "carol", "Carol White",  "carol"),
        ],
    )
    # ts = str(timestamp); timestamp = float(ts)
    messages = [
        # (ts, channel_id, user_id, username, text, timestamp, thread_ts)
        ("1000.000", "C001", "U001", "alice", "Hello world", 1000.0, None),
        ("1001.000", "C001", "U002", "bob",   "Error: out of memory", 1001.0, None),
        ("1002.000", "C002", "U001", "alice", "Deployed the service", 1002.0, None),
        ("1003.000", "C002", "U003", "carol", "WARNING: disk usage high", 1003.0, None),
        ("1004.000", "C003", "U002", "bob",   "Just a random note", 1004.0, None),
        ("1005.000", "C001", "U001", "alice", "Error in production!", 1005.0, None),
        ("1006.000", "C001", "U002", "bob",   "Reply to thread", 1006.0, "1001.000"),
    ]
    db.executemany(
        "INSERT OR IGNORE INTO messages(ts, channel_id, user_id, username, text, timestamp, thread_ts) "
        "VALUES (?, ?, ?, ?, ?, ?, ?)",
        messages,
    )
    db.commit()
    return db


# ── Fixed-string search ───────────────────────────────────────────────────────

def test_fixed_string_match(conn):
    results = grep_messages(conn, fixed_string="error")
    assert len(results) == 2
    texts = {r["text"] for r in results}
    assert "Error: out of memory" in texts
    assert "Error in production!" in texts


def test_fixed_string_case_insensitive(conn):
    results = grep_messages(conn, fixed_string="ERROR")
    assert len(results) == 2


def test_fixed_string_no_match(conn):
    results = grep_messages(conn, fixed_string="xyzzy")
    assert results == []


# ── Regexp search ─────────────────────────────────────────────────────────────

def test_regexp_match(conn):
    results = grep_messages(conn, pattern=r"error|warning")
    assert len(results) == 3
    texts = {r["text"] for r in results}
    assert "Error: out of memory" in texts
    assert "WARNING: disk usage high" in texts
    assert "Error in production!" in texts


def test_regexp_case_insensitive(conn):
    results = grep_messages(conn, pattern=r"^error")
    assert len(results) == 2


def test_regexp_anchored(conn):
    results = grep_messages(conn, pattern=r"^Hello")
    assert len(results) == 1
    assert results[0]["text"] == "Hello world"


# ── Channel filter ────────────────────────────────────────────────────────────

def test_channel_filter_by_name(conn):
    results = grep_messages(conn, fixed_string="error", channels=("general",))
    assert len(results) == 2
    assert all(r["channel"] == "general" for r in results)


def test_channel_filter_by_id(conn):
    results = grep_messages(conn, fixed_string="error", channels=("C001",))
    assert len(results) == 2


def test_channel_filter_multiple(conn):
    results = grep_messages(conn, pattern=r"error|warning", channels=("general", "engineering"))
    assert len(results) == 3


def test_channel_filter_unknown_raises(conn):
    with pytest.raises(ValueError, match="not found"):
        grep_messages(conn, fixed_string="error", channels=("nonexistent",))


def test_channel_filter_excludes_other_channels(conn):
    results = grep_messages(conn, fixed_string="error", channels=("engineering",))
    assert results == []


# ── Time filter ───────────────────────────────────────────────────────────────

def test_since_filter(conn):
    results = grep_messages(conn, fixed_string="error", since="1002.0")
    assert len(results) == 1
    assert results[0]["text"] == "Error in production!"


def test_until_filter(conn):
    results = grep_messages(conn, fixed_string="error", until="1002.0")
    assert len(results) == 1
    assert results[0]["text"] == "Error: out of memory"


def test_since_until_range(conn):
    results = grep_messages(conn, pattern=r".*", since="1001.0", until="1003.0")
    assert len(results) == 3
    assert {r["ts"] for r in results} == {"1001.000", "1002.000", "1003.000"}


# ── Person filter ─────────────────────────────────────────────────────────────

def test_person_filter_by_real_name(conn):
    results = grep_messages(conn, fixed_string="error", person="Alice")
    assert len(results) == 1
    assert results[0]["author"] == "Alice Smith"


def test_person_filter_by_handle(conn):
    results = grep_messages(conn, fixed_string="error", person="bob")
    assert len(results) == 1
    assert results[0]["text"] == "Error: out of memory"


def test_person_filter_no_match(conn):
    results = grep_messages(conn, fixed_string="error", person="carol")
    assert results == []


# ── Combined filters ──────────────────────────────────────────────────────────

def test_combined_channel_and_person(conn):
    results = grep_messages(conn, pattern=r".*", channels=("general",), person="bob")
    texts = {r["text"] for r in results}
    assert "Error: out of memory" in texts
    assert "Reply to thread" in texts
    assert "Hello world" not in texts


def test_combined_all_filters(conn):
    results = grep_messages(
        conn,
        fixed_string="error",
        channels=("general",),
        since="1000.0",
        until="1003.0",
        person="bob",
    )
    assert len(results) == 1
    assert results[0]["text"] == "Error: out of memory"


# ── Limit ─────────────────────────────────────────────────────────────────────

def test_limit(conn):
    results = grep_messages(conn, pattern=r".*", limit=2)
    assert len(results) == 2


# ── Validation ────────────────────────────────────────────────────────────────

def test_no_pattern_raises(conn):
    with pytest.raises(ValueError, match="--string or --regexp"):
        grep_messages(conn)


def test_both_patterns_raises(conn):
    with pytest.raises(ValueError, match="mutually exclusive"):
        grep_messages(conn, fixed_string="x", pattern="x")
