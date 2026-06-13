"""Slack Search — Streamlit web UI."""

import json
import re
import sqlite3
import os
from pathlib import Path

import pandas as pd
import streamlit as st
from dotenv import load_dotenv
from openai import OpenAI

from slack_search.database import open_db_readonly
from slack_search.conversations_db import (
    CURRENT_USER,
    open_conversations_db,
    list_conversations,
    create_conversation,
    rename_conversation,
    delete_conversation,
    load_messages,
    append_message,
    auto_title,
)

load_dotenv()

# ── Config ───────────────────────────────────────────────────────────────────

OPENCODE_API_KEY = os.getenv("OPENCODE_API_KEY", "").strip()
OPENCODE_BASE_URL = "https://opencode.ai/zen/go/v1"
OPENCODE_MODELS = ["qwen3.6-plus", "qwen3.5-plus"]

LM_STUDIO_HOST = os.getenv("LM_STUDIO_HOST", "localhost")
LM_STUDIO_PORT = os.getenv("LM_STUDIO_PORT", "1234")
LM_STUDIO_BASE_URL = f"http://{LM_STUDIO_HOST}:{LM_STUDIO_PORT}/v1"

RHT_MODELS_FILE = Path(__file__).parent / ".rht_models.json"


def _load_rht_models() -> tuple[str, dict[str, str]]:
    """Return (base_url, {model_name: api_key}) from .rht_models.json, or empty if missing."""
    if not RHT_MODELS_FILE.exists():
        return "", {}
    data = json.loads(RHT_MODELS_FILE.read_text())
    return data.get("base_url", ""), data.get("models", {})


@st.cache_data(ttl=60)
def _fetch_lm_studio_models() -> list[str]:
    """Try to list models from LM Studio; return empty list if unreachable."""
    try:
        import requests as _req
        resp = _req.get(f"{LM_STUDIO_BASE_URL}/models", timeout=2)
        if resp.ok:
            data = resp.json()
            return [m["id"] for m in data.get("data", [])]
    except Exception:
        pass
    return []

DEFAULT_DB = Path.home() / ".slack-search" / "messages.db"
CONV_DB_PATH = Path.home() / ".slack-search" / "conversations.db"
PROMPT_PATH = Path(__file__).parent / "prompts" / "nl_to_sql.md"

MAX_LLM_ROWS = 100  # max rows forwarded to the LLM in synthesise mode
SYNTHESISE_MARKER = "[SYNTHESISE]"


def load_system_prompt() -> str:
    if PROMPT_PATH.exists():
        return PROMPT_PATH.read_text()
    from slack_search.search import SCHEMA_DESCRIPTION
    return f"You are a SQL expert for a Slack message archive in SQLite.\n\n{SCHEMA_DESCRIPTION}"


def _extract_sql(text: str) -> str | None:
    m = re.search(r"```sql\s*(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1).strip()
    m = re.search(r"(SELECT\s.+?)(?:;|$)", text, re.DOTALL | re.IGNORECASE)
    return m.group(1).strip() if m else None


def make_client(provider: str, model: str = "") -> OpenAI:
    if provider == "OpenCode.ai":
        return OpenAI(api_key=OPENCODE_API_KEY, base_url=OPENCODE_BASE_URL)
    if provider == "LM Studio (local)":
        return OpenAI(api_key="local", base_url=LM_STUDIO_BASE_URL)
    if provider == "RHT models.corp":
        base_url, model_keys = _load_rht_models()
        api_key = model_keys.get(model, "")
        return OpenAI(api_key=api_key, base_url=base_url)
    raise ValueError(f"Unknown provider: {provider}")


def _cap_sql(sql: str) -> str:
    """Wrap SQL in a subquery to hard-cap rows at MAX_LLM_ROWS."""
    return f"SELECT * FROM ({sql.rstrip(';')}) _q LIMIT {MAX_LLM_ROWS}"


def _results_to_text(df: pd.DataFrame) -> str:
    """Format a DataFrame as a compact markdown table for the LLM."""
    if df.empty:
        return "(no rows returned)"
    header = " | ".join(str(c) for c in df.columns)
    sep = " | ".join("---" for _ in df.columns)
    rows = "\n".join(
        " | ".join(str(v) for v in row) for row in df.itertuples(index=False)
    )
    note = f"\n\n_(results capped at {MAX_LLM_ROWS} rows)_" if len(df) == MAX_LLM_ROWS else ""
    return f"{header}\n{sep}\n{rows}{note}"


def _generate_title(client: OpenAI, model: str, question: str, answer: str) -> str:
    """Ask the LLM for a short conversation title based on the first exchange."""
    try:
        resp = client.chat.completions.create(
            model=model,
            temperature=0,
            messages=[
                {
                    "role": "system",
                    "content": (
                        "You generate short conversation titles. "
                        "Reply with ONLY the title — no quotes, no punctuation at the end, "
                        "5 words maximum."
                    ),
                },
                {
                    "role": "user",
                    "content": f"Question: {question}\nAnswer summary: {answer[:300]}",
                },
            ],
        )
        title = resp.choices[0].message.content.strip().strip('"').strip("'")
        return title[:80] if title else question[:60]
    except Exception:
        return question[:60]


# ── Cached DB connections ─────────────────────────────────────────────────────

@st.cache_resource
def get_messages_conn(path: str) -> sqlite3.Connection | None:
    p = Path(path)
    return open_db_readonly(p) if p.exists() else None


@st.cache_resource
def get_conv_conn() -> sqlite3.Connection:
    return open_conversations_db(CONV_DB_PATH)


# ── Page setup ────────────────────────────────────────────────────────────────

st.set_page_config(page_title="Slack Search", page_icon="🔍", layout="wide")


# ── Sidebar ───────────────────────────────────────────────────────────────────

def render_sidebar(conv_conn: sqlite3.Connection) -> tuple[sqlite3.Connection | None, str | None, str, str]:
    with st.sidebar:
        st.title("🔍 Slack Search")

        # ── Conversation list ─────────────────────────────────────────────────
        st.subheader("Conversations")

        if st.button("＋ New conversation", width="stretch"):
            cid = create_conversation(conv_conn)
            st.session_state.conversation_id = cid
            st.session_state.nlq_messages = []
            st.rerun()

        conversations = list_conversations(conv_conn)
        active_id = st.session_state.get("conversation_id")

        for conv in conversations:
            cid = conv["id"]
            label = conv["title"]
            is_active = cid == active_id

            col_btn, col_del = st.columns([5, 1])
            with col_btn:
                if st.button(
                    label,
                    key=f"conv_{cid}",
                    width="stretch",
                    help=f"id: {cid}",
                    type="primary" if is_active else "secondary",
                ):
                    if not is_active:
                        st.session_state.conversation_id = cid
                        st.session_state.nlq_messages = None  # trigger reload
                        st.rerun()
            with col_del:
                if st.button("✕", key=f"del_{cid}", help="Delete conversation"):
                    delete_conversation(conv_conn, cid)
                    if is_active:
                        st.session_state.pop("conversation_id", None)
                        st.session_state.nlq_messages = None
                    st.rerun()

        st.divider()

        # ── Archive settings ──────────────────────────────────────────────────
        db_path = st.text_input("Archive database", value=str(DEFAULT_DB))
        msg_conn = get_messages_conn(db_path)

        if msg_conn is None:
            st.error("Database not found. Run `slack-search download` first.")

        channel_filter = None
        if msg_conn:
            rows = msg_conn.execute("SELECT id, name FROM channels ORDER BY name").fetchall()
            channels = {r["name"]: r["id"] for r in rows}
            if channels:
                selected = st.selectbox("Channel", ["(all)"] + list(channels.keys()))
                channel_filter = channels.get(selected)

        st.divider()

        # ── LLM ───────────────────────────────────────────────────────────────
        providers = []
        if OPENCODE_API_KEY:
            providers.append("OpenCode.ai")
        lm_studio_models = _fetch_lm_studio_models()
        if lm_studio_models:
            providers.append("LM Studio (local)")
        rht_base_url, rht_model_keys = _load_rht_models()
        if rht_model_keys:
            providers.append("RHT models.corp")

        if not providers:
            st.error("No LLM configured. Add OPENCODE_API_KEY to .env, start LM Studio, or add .rht_models.json.")
            provider = model = ""
        else:
            provider = st.selectbox("Provider", providers)
            if provider == "LM Studio (local)":
                model = st.selectbox("Model", lm_studio_models)
            elif provider == "RHT models.corp":
                model = st.selectbox("Model", list(rht_model_keys.keys()))
            else:
                model = st.selectbox("Model", OPENCODE_MODELS)

        st.divider()
        from slack_search.search import SCHEMA_DESCRIPTION
        with st.expander("Schema reference"):
            st.code(SCHEMA_DESCRIPTION, language="")

    return msg_conn, channel_filter, provider, model


# ── NL Query tab ─────────────────────────────────────────────────────────────

def _hydrate_messages(messages: list[dict], conn: sqlite3.Connection | None) -> list[dict]:
    """Re-run saved SQL to restore DataFrames after loading from DB."""
    for msg in messages:
        if "sql" in msg and "df" not in msg and conn:
            try:
                msg["df"] = pd.read_sql_query(msg["sql"], conn)
            except Exception:
                pass
    return messages


def render_nlq(
    msg_conn: sqlite3.Connection | None,
    conv_conn: sqlite3.Connection,
    channel_filter: str | None,
    provider: str,
    model: str,
) -> None:
    # Ensure a conversation is selected
    if "conversation_id" not in st.session_state:
        convs = list_conversations(conv_conn)
        if convs:
            st.session_state.conversation_id = convs[0]["id"]
        else:
            st.session_state.conversation_id = create_conversation(conv_conn)

    cid = st.session_state.conversation_id

    # Load messages from DB if not yet in session (first load or conversation switched)
    if st.session_state.get("nlq_messages") is None:
        raw = load_messages(conv_conn, cid)
        st.session_state.nlq_messages = _hydrate_messages(raw, msg_conn)

    messages: list[dict] = st.session_state.nlq_messages

    # Render history
    for msg in messages:
        with st.chat_message(msg["role"]):
            st.markdown(msg["content"])
            if "sql" in msg:
                with st.expander("Generated SQL"):
                    st.code(msg["sql"], language="sql")
            if "df" in msg:
                st.dataframe(msg["df"], width="stretch")

    # Input
    prompt = st.chat_input("Ask anything about your Slack archive…")
    if not prompt:
        return
    if not provider:
        st.error("Configure a provider in the sidebar first.")
        return
    if not msg_conn:
        st.error("No archive database connected.")
        return

    # Build augmented prompt with channel context
    if channel_filter:
        row = msg_conn.execute("SELECT name FROM channels WHERE id=?", (channel_filter,)).fetchone()
        ch_name = row["name"] if row else channel_filter
        augmented = f"[Only consider channel '{ch_name}' (id={channel_filter})]\n\n{prompt}"
    else:
        augmented = prompt

    is_first_message = len(messages) == 0

    # Save and display user message
    append_message(conv_conn, cid, "user", prompt)
    messages.append({"role": "user", "content": prompt})
    with st.chat_message("user"):
        st.markdown(prompt)

    # Build API message list (send content only, not df/sql metadata)
    system_prompt = load_system_prompt()
    api_msgs = [{"role": "system", "content": system_prompt}]
    for m in messages[:-1]:
        api_msgs.append({"role": m["role"], "content": m["content"]})
    api_msgs.append({"role": "user", "content": augmented})

    # Stream response
    client = make_client(provider, model)
    with st.chat_message("assistant"):
        placeholder = st.empty()
        full_response = ""
        try:
            stream = client.chat.completions.create(
                model=model or OPENCODE_MODELS[0],
                messages=api_msgs,
                temperature=0,
                stream=True,
            )
            for chunk in stream:
                if chunk.choices and chunk.choices[0].delta.content:
                    full_response += chunk.choices[0].delta.content
                    placeholder.markdown(full_response + "▌")
            placeholder.markdown(full_response)
        except Exception as e:
            placeholder.error(f"LLM error: {e}")
            return

        synthesise = SYNTHESISE_MARKER in full_response
        # Strip the marker from what's displayed and stored
        display_response = full_response.replace(SYNTHESISE_MARKER, "").lstrip("\n").strip()
        placeholder.markdown(display_response)

        sql = _extract_sql(full_response)
        df = None
        nl_answer = None
        if sql:
            with st.expander("Generated SQL", expanded=synthesise):
                st.code(sql, language="sql")
            try:
                if synthesise:
                    df = pd.read_sql_query(_cap_sql(sql), msg_conn)
                    results_text = _results_to_text(df)
                    synthesis_msgs = [
                        {"role": "system", "content": (
                            "You are a helpful assistant analysing Slack archive query results. "
                            "The SQL query was already run and the results below are the complete, "
                            "correct dataset for answering the question — trust them fully. "
                            "Do not speculate about missing data, question the query, or caveat "
                            "whether the right rows were returned. "
                            "Answer the user's question directly and concisely based solely on "
                            "the rows provided."
                        )},
                        {"role": "user", "content": (
                            f"Query results:\n\n{results_text}\n\n"
                            f"Question: {prompt}"
                        )},
                    ]
                    nl_placeholder = st.empty()
                    nl_answer = ""
                    stream2 = client.chat.completions.create(
                        model=model or OPENCODE_MODELS[0],
                        messages=synthesis_msgs,
                        temperature=0,
                        stream=True,
                    )
                    for chunk in stream2:
                        if chunk.choices and chunk.choices[0].delta.content:
                            nl_answer += chunk.choices[0].delta.content
                            nl_placeholder.markdown(nl_answer + "▌")
                    nl_placeholder.markdown(nl_answer)
                    st.caption(f"Based on {len(df)} row(s), capped at {MAX_LLM_ROWS}")
                else:
                    df = pd.read_sql_query(sql, msg_conn)
                    st.dataframe(df, width="stretch")
                    st.caption(f"{len(df)} row(s)")
            except Exception as e:
                st.error(f"SQL error: {e}")

    # Store the NL answer when synthesised, otherwise the (marker-stripped) SQL response
    stored_content = nl_answer if nl_answer else display_response
    append_message(conv_conn, cid, "assistant", stored_content, sql=sql)

    record: dict = {"role": "assistant", "content": stored_content}
    if sql:
        record["sql"] = sql
    if df is not None:
        record["df"] = df
    messages.append(record)

    # Generate title from the first exchange and rerun so the sidebar reflects it
    if is_first_message:
        title = _generate_title(client, model or OPENCODE_MODELS[0], prompt, stored_content)
        rename_conversation(conv_conn, cid, title)
        st.rerun()


# ── Browse tab ────────────────────────────────────────────────────────────────

def render_browse(conn: sqlite3.Connection, channel_filter: str | None) -> None:
    st.subheader("Recent messages")

    col1, col2 = st.columns([1, 3])
    with col1:
        limit = st.selectbox("Show", [25, 50, 100, 200], index=0)
    with col2:
        keyword = st.text_input("Filter text", placeholder="optional keyword…")

    where_parts, params = [], []
    if channel_filter:
        where_parts.append("m.channel_id = ?")
        params.append(channel_filter)
    if keyword:
        where_parts.append("m.text LIKE ?")
        params.append(f"%{keyword}%")

    where = ("WHERE " + " AND ".join(where_parts)) if where_parts else ""
    sql = f"""
        SELECT
            datetime(m.timestamp, 'unixepoch') AS time,
            c.name AS channel,
            COALESCE(u.real_name, u.display_name, m.username, m.user_id, '(bot)') AS author,
            CASE WHEN m.thread_ts IS NOT NULL AND m.thread_ts != m.ts
                 THEN '↳ ' ELSE '' END || m.text AS text,
            m.reply_count
        FROM messages m
        LEFT JOIN users u ON m.user_id = u.id
        LEFT JOIN channels c ON m.channel_id = c.id
        {where}
        ORDER BY m.timestamp DESC
        LIMIT ?
    """
    params.append(limit)
    df = pd.read_sql_query(sql, conn, params=params)
    st.dataframe(df, width="stretch", height=600)
    st.caption(f"{len(df)} row(s) shown")


# ── SQL tab ───────────────────────────────────────────────────────────────────

def render_sql(conn: sqlite3.Connection, channel_filter: str | None) -> None:
    default = (
        "SELECT datetime(m.timestamp, 'unixepoch') AS time,\n"
        "       COALESCE(u.real_name, m.username, m.user_id) AS author,\n"
        "       m.text\n"
        "FROM messages m\n"
        "LEFT JOIN users u ON m.user_id = u.id\n"
    )
    if channel_filter:
        default += f"WHERE m.channel_id = '{channel_filter}'\n"
    default += "ORDER BY m.timestamp DESC\nLIMIT 25"

    sql_input = st.text_area("SQL", value=default, height=180)
    if st.button("Run", type="primary"):
        try:
            df = pd.read_sql_query(sql_input, conn)
            st.dataframe(df, width="stretch")
            st.caption(f"{len(df)} row(s)")
        except Exception as e:
            st.error(f"SQL error: {e}")


# ── Main ──────────────────────────────────────────────────────────────────────

def main() -> None:
    conv_conn = get_conv_conn()
    msg_conn, channel_filter, provider, model = render_sidebar(conv_conn)

    if msg_conn is None:
        st.info("Set the archive database path in the sidebar and run a download first.")
        st.code(
            'uv run slack-search download --curl "$(cat .curl)" '
            "--channel cost-mgmt-dev --since '3 weeks ago' --no-files",
            language="bash",
        )
        return

    tab_nlq, tab_browse, tab_sql = st.tabs([
        "💬 Ask in natural language",
        "📋 Browse messages",
        "🛠 SQL query",
    ])

    with tab_nlq:
        render_nlq(msg_conn, conv_conn, channel_filter, provider, model)

    with tab_browse:
        render_browse(msg_conn, channel_filter)

    with tab_sql:
        render_sql(msg_conn, channel_filter)


if __name__ == "__main__":
    main()
