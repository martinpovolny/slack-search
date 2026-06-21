"""Unit tests for slack_search.ai_query.run_query() error handling."""
import sqlite3
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, PropertyMock

import pytest
from openai import APIConnectionError, APIStatusError

from slack_search.ai_query import run_query
from slack_search.database import open_db

QUESTION = "who posts the most messages?"
MODEL = "test-model"
# Minimal system-prompt file so _load_system_prompt() doesn't fail
PROMPT_PATH = Path(__file__).parent / "fixtures" / "nl_to_sql_stub.md"


def _make_response(sql: str) -> SimpleNamespace:
    """Fake OpenAI ChatCompletion response containing a SQL block."""
    msg = SimpleNamespace(content=f"```sql\n{sql}\n```")
    choice = SimpleNamespace(message=msg)
    return SimpleNamespace(choices=[choice])


@pytest.fixture
def conn():
    db = open_db(":memory:")
    db.executemany(
        "INSERT OR IGNORE INTO channels(id, name) VALUES (?, ?)",
        [("C001", "general")],
    )
    db.executemany(
        "INSERT OR IGNORE INTO users(id, name, real_name, display_name) VALUES (?, ?, ?, ?)",
        [("U001", "alice", "Alice", "alice")],
    )
    db.execute(
        "INSERT OR IGNORE INTO messages(ts, channel_id, user_id, username, text, timestamp) "
        "VALUES ('1000.000', 'C001', 'U001', 'alice', 'hello', 1000.0)"
    )
    db.commit()
    return db


@pytest.fixture(autouse=True)
def stub_prompt(tmp_path, monkeypatch):
    """Point PROMPT_PATH at a minimal stub so the real prompts/ dir isn't needed."""
    stub = tmp_path / "nl_to_sql_stub.md"
    stub.write_text("You are a SQL assistant.")
    import slack_search.ai_query as aq
    monkeypatch.setattr(aq, "PROMPT_PATH", stub)


# ── Happy path ────────────────────────────────────────────────────────────────

def test_successful_table_query(conn):
    client = MagicMock()
    client.chat.completions.create.return_value = _make_response(
        "SELECT count(*) as n FROM messages"
    )
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "table"
    assert result.df is not None
    assert result.error is None


# ── Non-standard exception from API call ──────────────────────────────────────

def test_unexpected_exception_is_caught(conn):
    """RuntimeError (e.g. proxy failure) must not crash with AttributeError."""
    client = MagicMock()
    client.chat.completions.create.side_effect = RuntimeError("socks proxy exploded")
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"
    assert "socks proxy exploded" in result.error


def test_value_error_is_caught(conn):
    client = MagicMock()
    client.chat.completions.create.side_effect = ValueError("bad model name")
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"
    assert "bad model name" in result.error


# ── Bad response types ────────────────────────────────────────────────────────

def test_string_response_is_caught(conn):
    """String response (non-standard endpoint) must not crash with AttributeError."""
    client = MagicMock()
    client.chat.completions.create.return_value = "I am not a completion object"
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"
    assert "unexpected" in result.error.lower()


def test_empty_choices_is_caught(conn):
    """Response with empty choices list should return a clean error."""
    client = MagicMock()
    client.chat.completions.create.return_value = SimpleNamespace(choices=[])
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"
    assert "unexpected" in result.error.lower()


def test_none_response_is_caught(conn):
    client = MagicMock()
    client.chat.completions.create.return_value = None
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"


# ── Known exception types ─────────────────────────────────────────────────────

def test_api_connection_error_is_caught(conn):
    client = MagicMock()
    client.chat.completions.create.side_effect = APIConnectionError(
        request=MagicMock()
    )
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"


def test_api_status_error_is_caught(conn):
    client = MagicMock()
    mock_response = MagicMock()
    mock_response.status_code = 401
    mock_response.headers = {}
    client.chat.completions.create.side_effect = APIStatusError(
        "Unauthorized", response=mock_response, body=None
    )
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "error"


# ── Synthesis phase error handling ────────────────────────────────────────────

def test_synthesis_unexpected_exception(conn):
    """Synthesis phase crash is isolated — table result is still returned."""
    client = MagicMock()
    # Phase 1 returns a SYNTHESISE response
    phase1 = _make_response("[SYNTHESISE]\nSELECT count(*) as n FROM messages")
    phase1.choices[0].message.content = "[SYNTHESISE]\n```sql\nSELECT count(*) as n FROM messages\n```"
    client.chat.completions.create.side_effect = [
        phase1,
        RuntimeError("synthesis backend unavailable"),
    ]
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "synthesise"
    assert result.error is not None
    assert "synthesis backend unavailable" in result.error


def test_synthesis_string_response(conn):
    """String from synthesis phase sets error, does not crash."""
    client = MagicMock()
    phase1 = SimpleNamespace(
        choices=[SimpleNamespace(
            message=SimpleNamespace(
                content="[SYNTHESISE]\n```sql\nSELECT count(*) as n FROM messages\n```"
            )
        )]
    )
    client.chat.completions.create.side_effect = [
        phase1,
        "not a completion object",
    ]
    result = run_query(conn, QUESTION, client, MODEL)
    assert result.mode == "synthesise"
    assert result.error is not None
