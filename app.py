"""Slack Search — Streamlit web UI."""

import json
import logging
import re
import sqlite3
import os
import traceback
from pathlib import Path

logging.basicConfig(
    level=logging.DEBUG,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("slack_search.app")

import httpx

import pandas as pd
import streamlit as st
from dotenv import load_dotenv
from openai import OpenAI

from slack_search.database import open_db, open_db_readonly
from slack_search.slack_format import build_user_map, extract_uids, resolve_mentions, resolve_mentions_html, highlight_matches_html
from slack_search.slack_search_api import run_slack_search, extract_highlight_term
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

LITE_MAAS_KEY = os.getenv("LITE_MAAS_KEY", "").strip()
LITE_MAAS_BASE_URL = os.getenv("LITE_MAAS_BASE_URL", "").strip()

LM_STUDIO_HOST = os.getenv("LM_STUDIO_HOST", "localhost")
LM_STUDIO_PORT = os.getenv("LM_STUDIO_PORT", "1234")
LM_STUDIO_BASE_URL = f"http://{LM_STUDIO_HOST}:{LM_STUDIO_PORT}/v1"

RHT_MODELS_FILE = Path(__file__).parent / ".rht_models.json"


def _load_rht_models() -> tuple[str, dict]:
    """Return (url_template, models_dict) from .rht_models.json, or empty if missing."""
    if not RHT_MODELS_FILE.exists():
        return "", {}
    data = json.loads(RHT_MODELS_FILE.read_text())
    return data.get("url_template", ""), data.get("models", {})


@st.cache_data(ttl=300)
def _fetch_litemaas_models() -> list[str]:
    """Fetch chat-capable models from LiteMaaS; return empty list if unreachable."""
    try:
        import requests as _req
        resp = _req.get(
            f"{LITE_MAAS_BASE_URL}/models",
            headers={"Authorization": f"Bearer {LITE_MAAS_KEY}"},
            timeout=5, verify=False,
        )
        if resp.ok:
            data = resp.json()
            return [m["id"] for m in data.get("data", [])
                    if m.get("id") and m["id"] != "Nomic-embed-text-v2-moe"]
    except Exception:
        pass
    return []


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
SYNTHESIS_PROMPT_PATH = Path(__file__).parent / "prompts" / "synthesis.md"

MAX_LLM_ROWS = 100  # max rows forwarded to the LLM in synthesise mode
SYNTHESISE_MARKER = "[SYNTHESISE]"


def load_system_prompt(conn: sqlite3.Connection | None = None) -> str:
    from datetime import date as _date
    text = PROMPT_PATH.read_text() if PROMPT_PATH.exists() else (
        "You are a SQL expert for a Slack message archive in SQLite.\n\n"
    )
    today = _date.today().strftime("%A, %Y-%m-%d")
    archive_range = ""
    if conn:
        try:
            row = conn.execute(
                "SELECT date(min(timestamp), 'unixepoch') as oldest, "
                "date(max(timestamp), 'unixepoch') as newest FROM messages"
            ).fetchone()
            if row and row[0]:
                archive_range = f"Archive date range: {row[0]} to {row[1]}. "
        except Exception:
            pass
    header = f"Today is {today}. {archive_range}When the user mentions a date without a year, use a year within the archive range.\n\n"
    return header + text


def load_synthesis_prompt() -> str:
    from datetime import date as _date
    today_str = _date.today().strftime("%A, %Y-%m-%d")
    if SYNTHESIS_PROMPT_PATH.exists():
        return SYNTHESIS_PROMPT_PATH.read_text().replace("{today}", today_str)
    return (
        f"You are a helpful assistant analysing Slack archive query results. Today is {today_str}. "
        "Answer the user's question directly and concisely based on the SQL results provided."
    )


def _extract_sql(text: str) -> str | None:
    m = re.search(r"```sql\s*(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1).strip()
    m = re.search(r"(SELECT\s.+?)(?:;|$)", text, re.DOTALL | re.IGNORECASE)
    return m.group(1).strip() if m else None


def api_model_id(provider: str, model: str) -> str:
    """Return the model string expected by the API (may differ from display name)."""
    if provider == "RHT models.corp":
        _, models = _load_rht_models()
        entry = models.get(model, {})
        return entry.get("api_model_id", model)
    return model


def _http_client() -> httpx.Client | None:
    """Return an httpx client with proxy and/or SSL verification disabled if configured."""
    proxy = os.getenv("HTTPS_PROXY") or os.getenv("ALL_PROXY")
    verify = os.getenv("SSL_NO_VERIFY", "").lower() not in ("1", "true", "yes")
    if proxy:
        return httpx.Client(proxy=proxy, verify=verify)
    if not verify:
        return httpx.Client(verify=False)
    return None


def make_client(provider: str, model: str = "") -> OpenAI:
    http = _http_client()
    kwargs = {"http_client": http} if http else {}
    if provider == "LiteMaaS":
        return OpenAI(api_key=LITE_MAAS_KEY, base_url=LITE_MAAS_BASE_URL, **kwargs)
    if provider == "LM Studio (local)":
        return OpenAI(api_key="local", base_url=LM_STUDIO_BASE_URL, **kwargs)
    if provider == "RHT models.corp":
        url_template, models = _load_rht_models()
        base_url = url_template.format(model=model)
        api_key = models.get(model, {}).get("key", "")
        return OpenAI(api_key=api_key, base_url=base_url, **kwargs)
    raise ValueError(f"Unknown provider: {provider}")


def _slack_permalink(workspace: str, channel_id: str, ts: str, thread_ts: str | None = None) -> str:
    """Build a Slack web URL from channel_id + ts (no API call needed)."""
    ts_nodot = ts.replace(".", "")
    url = f"https://{workspace}/archives/{channel_id}/p{ts_nodot}"
    if thread_ts and thread_ts != ts:
        url += f"?thread_ts={thread_ts}&ctype=thread"
    return url


def _slack_app_link(web_url: str) -> str:
    """Convert a Slack https:// URL to a slack:// deep-link that opens the desktop app."""
    from urllib.parse import quote
    return f"slack://open?url={quote(web_url, safe='')}"


def _cap_sql(sql: str, limit: int = MAX_LLM_ROWS) -> str:
    """Wrap SQL in a subquery to cap rows sent to the LLM."""
    return f"SELECT * FROM ({sql.rstrip(';')}) _q LIMIT {limit}"


def _results_to_text(df: pd.DataFrame, conn: sqlite3.Connection | None = None) -> str:
    """Format a DataFrame as a compact markdown table for the LLM."""
    if df.empty:
        return "(no rows returned)"
    if conn is not None:
        text_cols = [c for c in df.columns if "text" in c.lower() or "message" in c.lower()]
        if text_cols:
            all_texts = [str(v) for col in text_cols for v in df[col] if v]
            uids = extract_uids(all_texts)
            user_map = build_user_map(conn, uids)
            df = df.copy()
            for col in text_cols:
                df[col] = df[col].apply(lambda v: resolve_mentions(str(v), user_map) if v else v)
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


# ── Slack client helpers ──────────────────────────────────────────────────────

def _make_slack_client_from_env():
    """Build a SlackClient from env vars, or None if no token is set."""
    from slack_search.slack_client import SlackClient
    token = os.getenv("SLACK_TOKEN", "").strip()
    if not token:
        return None
    cookie = os.getenv("SLACK_COOKIE", "").strip() or None
    workspace = os.getenv("SLACK_WORKSPACE", "").strip() or None
    return SlackClient(token=token, cookie=cookie, workspace=workspace)


def _make_slack_client_from_curl(curl_path: str):
    """Build a SlackClient by parsing a saved curl command file."""
    from slack_search.curl_parser import parse_curl
    from slack_search.slack_client import SlackClient
    text = Path(curl_path).read_text()
    creds = parse_curl(text)
    return SlackClient(token=creds.token, workspace=creds.workspace, raw_cookies=creds.raw_cookies), creds.workspace


# ── Cached DB connections ─────────────────────────────────────────────────────

@st.cache_resource
def get_messages_conn(path: str) -> sqlite3.Connection | None:
    p = Path(path)
    if not p.exists():
        log.warning("DB not found at %s", path)
        return None
    conn = open_db_readonly(p)
    log.info("Opened readonly connection to %s (id=%s)", path, id(conn))
    return conn



@st.cache_resource
def get_conv_conn() -> sqlite3.Connection:
    return open_conversations_db(CONV_DB_PATH)


# ── Page setup ────────────────────────────────────────────────────────────────

st.set_page_config(page_title="Slack Search", page_icon="🔍", layout="wide")


# ── Sidebar ───────────────────────────────────────────────────────────────────

def render_sidebar(conv_conn: sqlite3.Connection) -> tuple[sqlite3.Connection | None, str | None, str, str, str]:
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
        rht_base_url, rht_model_keys = _load_rht_models()
        if rht_model_keys:
            providers.append("RHT models.corp")
        litemaas_models = _fetch_litemaas_models() if LITE_MAAS_KEY and LITE_MAAS_BASE_URL else []
        if litemaas_models:
            providers.append("LiteMaaS")
        lm_studio_models = _fetch_lm_studio_models()
        if lm_studio_models:
            providers.append("LM Studio (local)")

        if not providers:
            st.error("No LLM configured. Add LITE_MAAS_KEY to .env, start LM Studio, or add .rht_models.json.")
            provider = model = ""
        else:
            provider = st.selectbox("Provider", providers)
            if provider == "LM Studio (local)":
                model = st.selectbox("Model", lm_studio_models)
            elif provider == "RHT models.corp":
                model = st.selectbox("Model", list(rht_model_keys.keys()))
            elif provider == "LiteMaaS":
                model = st.selectbox("Model", litemaas_models)
            else:
                model = st.selectbox("Model", [])

        st.divider()

        # ── Live search credentials ───────────────────────────────────────────
        st.subheader("Live search")
        # Auto-init: env vars first, then fall back to .curl file if it exists
        if "slack_client" not in st.session_state:
            client = _make_slack_client_from_env()
            if client:
                st.session_state["slack_client"] = client
                st.session_state["slack_workspace"] = os.getenv("SLACK_WORKSPACE", "slack.com")
            elif Path(".curl").exists():
                try:
                    client, workspace = _make_slack_client_from_curl(".curl")
                    st.session_state["slack_client"] = client
                    st.session_state["slack_workspace"] = workspace
                except Exception:
                    pass

        if "slack_client" in st.session_state:
            ws = st.session_state.get("slack_workspace", "")
            st.caption(f"Connected: {ws or 'slack.com'}")

        curl_path = st.text_input(".curl file", value=".curl", label_visibility="collapsed",
                                  placeholder=".curl file path…")
        if st.button("Load credentials", use_container_width=True):
            try:
                client, workspace = _make_slack_client_from_curl(curl_path)
                st.session_state["slack_client"] = client
                st.session_state["slack_workspace"] = workspace
                st.rerun()
            except Exception as e:
                st.error(f"Failed: {e}")

        st.divider()
        from slack_search.search import SCHEMA_DESCRIPTION
        with st.expander("Schema reference"):
            st.code(SCHEMA_DESCRIPTION, language="")

    return msg_conn, channel_filter, provider, model, db_path


# ── NL Query tab ─────────────────────────────────────────────────────────────

def _hydrate_messages(messages: list[dict], conn: sqlite3.Connection | None) -> list[dict]:
    """Re-run saved SQL to restore DataFrames after loading from DB."""
    log.debug("_hydrate_messages: %d messages, conn=%s", len(messages), id(conn) if conn else None)
    for msg in messages:
        if "sql" in msg and "df" not in msg and conn:
            sql = msg["sql"]
            log.debug("Hydrating SQL: %s", sql[:120])
            try:
                msg["df"] = pd.read_sql_query(sql, conn)
                log.info("Hydrated OK: %d rows", len(msg["df"]))
            except Exception as exc:
                log.error("Hydration FAILED: %s\nSQL: %s\n%s", exc, sql, traceback.format_exc())
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
        log.info("Loading conversation %s, msg_conn=%s", cid, id(msg_conn) if msg_conn else None)
        raw = load_messages(conv_conn, cid)
        st.session_state.nlq_messages = _hydrate_messages(raw, msg_conn)

    messages: list[dict] = st.session_state.nlq_messages

    # Handle pending synthesis (triggered by "Summarise" button on a past message)
    if pending := st.session_state.pop("pending_synthesise", None):
        client = make_client(provider, model)
        effective_model = api_model_id(provider, model)
        sql = pending["sql"]
        question = pending["question"]
        df = pending["df"]
        results_text = _results_to_text(df, msg_conn)
        synthesis_msgs = [
            {"role": "system", "content": load_synthesis_prompt()},
            {"role": "user", "content": (
                f"SQL query that produced these results:\n```sql\n{sql}\n```\n\n"
                f"Query results:\n\n{results_text}\n\n"
                f"Question: {question}"
            )},
        ]
        nl_answer = ""
        with st.spinner("Summarising…"):
            try:
                stream = client.chat.completions.create(
                    model=effective_model, messages=synthesis_msgs, temperature=0, stream=True,
                )
                for chunk in stream:
                    if chunk.choices and chunk.choices[0].delta.content:
                        nl_answer += chunk.choices[0].delta.content
            except Exception as e:
                st.error(f"Synthesis error: {e}")
        if nl_answer:
            idx = pending["msg_index"]
            messages[idx]["nl_answer"] = nl_answer
            messages[idx]["content"] = nl_answer
            append_message(conv_conn, cid, "assistant", nl_answer, sql=sql)
        st.rerun()

    # Render history
    for i, msg in enumerate(messages):
        with st.chat_message(msg["role"]):
            st.markdown(msg["content"])
            if "sql" in msg:
                with st.expander("Generated SQL"):
                    st.code(msg["sql"], language="sql")
            if "df" in msg:
                st.dataframe(msg["df"], width="stretch")
            # Show synthesis button for table-mode assistant messages without an NL answer
            if (msg["role"] == "assistant" and "sql" in msg and "df" in msg
                    and "nl_answer" not in msg and i > 0):
                user_q = next(
                    (m["content"] for m in reversed(messages[:i]) if m["role"] == "user"), ""
                )
                if st.button("✨ Summarise with AI", key=f"synth_{i}"):
                    st.session_state["pending_synthesise"] = {
                        "sql": msg["sql"], "question": user_q,
                        "df": msg["df"], "msg_index": i,
                    }
                    st.rerun()

    # Per-conversation row limit for synthesise mode
    row_limit = st.selectbox(
        "Max rows sent to LLM",
        [100, 500, 1000],
        index=[100, 500, 1000].index(st.session_state.get(f"row_limit_{cid}", MAX_LLM_ROWS)),
        key=f"row_limit_sel_{cid}",
    )
    st.session_state[f"row_limit_{cid}"] = row_limit

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
    system_prompt = load_system_prompt(msg_conn)
    api_msgs = [{"role": "system", "content": system_prompt}]
    for m in messages[:-1]:
        api_msgs.append({"role": m["role"], "content": m["content"]})
    api_msgs.append({"role": "user", "content": augmented})

    # Stream response
    client = make_client(provider, model)
    effective_model = api_model_id(provider, model)
    with st.chat_message("assistant"):
        placeholder = st.empty()
        full_response = ""
        try:
            stream = client.chat.completions.create(
                model=effective_model,
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
                    df = pd.read_sql_query(_cap_sql(sql, row_limit), msg_conn)
                    results_text = _results_to_text(df, msg_conn)
                    synthesis_msgs = [
                        {"role": "system", "content": load_synthesis_prompt()},
                        {"role": "user", "content": (
                            f"SQL query that produced these results:\n```sql\n{sql}\n```\n\n"
                            f"Query results:\n\n{results_text}\n\n"
                            f"Question: {prompt}"
                        )},
                    ]
                    nl_placeholder = st.empty()
                    nl_answer = ""
                    stream2 = client.chat.completions.create(
                        model=effective_model,
                        messages=synthesis_msgs,
                        temperature=0,
                        stream=True,
                    )
                    for chunk in stream2:
                        if chunk.choices and chunk.choices[0].delta.content:
                            nl_answer += chunk.choices[0].delta.content
                            nl_placeholder.markdown(nl_answer + "▌")
                    nl_placeholder.markdown(nl_answer)
                    st.caption(f"Based on {len(df)} row(s), capped at {row_limit}")
                else:
                    log.info("Executing SQL (fresh): %s", sql[:120])
                    df = pd.read_sql_query(sql, msg_conn)
                    log.info("Fresh SQL returned %d rows", len(df))
                    st.dataframe(df, width="stretch")
                    st.caption(f"{len(df)} row(s)")
            except Exception as e:
                log.error("SQL execution FAILED: %s\n%s", e, traceback.format_exc())
                st.error(f"SQL error: {e}")

    # Store the NL answer when synthesised, otherwise the (marker-stripped) SQL response
    stored_content = nl_answer if nl_answer else display_response
    append_message(conv_conn, cid, "assistant", stored_content, sql=sql)

    record: dict = {"role": "assistant", "content": stored_content}
    if sql:
        record["sql"] = sql
    if df is not None:
        record["df"] = df
    if nl_answer:
        record["nl_answer"] = nl_answer
    messages.append(record)

    # Generate title from the first exchange and rerun so the sidebar reflects it
    if is_first_message:
        title = _generate_title(client, effective_model, prompt, stored_content)
        rename_conversation(conv_conn, cid, title)
    # Always rerun so the history loop re-renders with the new message,
    # which is needed to show the "Summarise" button on table-mode responses
    st.rerun()


# ── Browse tab ────────────────────────────────────────────────────────────────

def render_browse(conn: sqlite3.Connection, channel_filter: str | None) -> None:
    import datetime as _dt

    st.subheader("Browse messages")

    # ── Filter row 1: text search ──────────────────────────────────
    fc1, fc2, fc3 = st.columns([4, 1, 1])
    with fc1:
        keyword = st.text_input("Search text", placeholder="leave empty for all messages…", label_visibility="collapsed")
    with fc2:
        use_regexp = st.checkbox("Regexp", value=False)
    with fc3:
        limit = st.selectbox("Rows", [25, 50, 100, 200], index=0)

    # ── Filter row 2: channel, person, dates ───────────────────────
    ch_rows = conn.execute(
        "SELECT id, COALESCE(name, id) AS name FROM channels ORDER BY name"
    ).fetchall()
    ch_options = [r[1] for r in ch_rows]
    ch_id_map = {r[1]: r[0] for r in ch_rows}

    fc1, fc2, fc3, fc4 = st.columns([2, 1, 1, 1])
    with fc1:
        sel_channels = st.multiselect("Channels", ch_options, placeholder="all channels")
    with fc2:
        person = st.text_input("Person", placeholder="partial name…")
    with fc3:
        since_date = st.date_input("Since", value=None)
    with fc4:
        until_date = st.date_input("Until", value=None)

    # ── Build query ────────────────────────────────────────────────
    if use_regexp and keyword:
        conn.create_function(
            "regexp", 2,
            lambda p, t: bool(re.search(p, t or "", re.IGNORECASE)),
        )

    where_parts: list[str] = []
    params: list = []

    if sel_channels:
        ids = [ch_id_map[n] for n in sel_channels]
        placeholders = ",".join("?" * len(ids))
        where_parts.append(f"m.channel_id IN ({placeholders})")
        params.extend(ids)
    elif channel_filter:
        where_parts.append("m.channel_id = ?")
        params.append(channel_filter)

    if keyword:
        if use_regexp:
            where_parts.append("m.text REGEXP ?")
            params.append(keyword)
        else:
            where_parts.append("m.text LIKE ?")
            params.append(f"%{keyword}%")

    if since_date:
        params.append(_dt.datetime.combine(since_date, _dt.time.min).timestamp())
        where_parts.append("m.timestamp >= ?")
    if until_date:
        params.append(_dt.datetime.combine(until_date, _dt.time.max).timestamp())
        where_parts.append("m.timestamp <= ?")

    if person:
        like = f"%{person}%"
        where_parts.append(
            "(u.name LIKE ? OR u.real_name LIKE ? OR u.display_name LIKE ? OR m.username LIKE ?)"
        )
        params.extend([like, like, like, like])

    where = ("WHERE " + " AND ".join(where_parts)) if where_parts else ""
    sql = f"""
        SELECT
            datetime(m.timestamp, 'unixepoch') AS time,
            c.name AS channel,
            COALESCE(u.real_name, u.display_name, m.username, m.user_id, '(bot)') AS author,
            CASE WHEN m.thread_ts IS NOT NULL AND m.thread_ts != m.ts
                 THEN '↳ ' ELSE '' END || m.text AS text,
            m.reply_count,
            m.ts         AS _ts,
            m.channel_id AS _channel_id,
            m.thread_ts  AS _thread_ts
        FROM messages m
        LEFT JOIN users u ON m.user_id = u.id
        LEFT JOIN channels c ON m.channel_id = c.id
        {where}
        ORDER BY m.timestamp DESC
        LIMIT ?
    """
    params.append(limit)
    df = pd.read_sql_query(sql, conn, params=params)

    # Resolve <@UXXXXXXX> mentions to real names in the text column
    user_map: dict[str, str] = {}
    if not df.empty:
        uids = extract_uids(df["text"].tolist())
        user_map = build_user_map(conn, uids)
        df["_raw_text"] = df["text"]
        df["text"] = df["text"].apply(lambda t: resolve_mentions(t or "", user_map))

    # Build Slack deep-links: encode permalink+time into the time column itself so
    # a LinkColumn with display_text regex can show the time while linking to Slack.
    workspace = st.session_state.get("slack_workspace", "")
    if workspace and not df.empty:
        df["_slack"] = df.apply(
            lambda r: _slack_permalink(workspace, r["_channel_id"], r["_ts"], r.get("_thread_ts")),
            axis=1,
        )
        df["time"] = df["_slack"].apply(_slack_app_link) + "#" + df["time"]

    display_cols = [c for c in df.columns if not c.startswith("_")]
    col_cfg: dict = {
        "channel":     st.column_config.TextColumn("Channel",   width="small"),
        "author":      st.column_config.TextColumn("Author",    width="small"),
        "reply_count": st.column_config.NumberColumn("Replies", width="small"),
    }
    if workspace and not df.empty:
        col_cfg["time"] = st.column_config.LinkColumn("Time", display_text=r"#(.+)", width="small")
    else:
        col_cfg["time"] = st.column_config.TextColumn("Time", width="small")

    event = st.dataframe(
        df[display_cols],
        width="stretch",
        height=400,
        on_select="rerun",
        selection_mode="single-row",
        column_config=col_cfg,
    )

    rows = event.selection.rows if event and event.selection else []
    st.caption(f"{len(df)} row(s) shown — click a row to read the full message below")

    if rows:
        row = df.iloc[rows[0]]
        raw = row.get("_raw_text") or row["text"]
        html_text = resolve_mentions_html(raw, user_map)
        html_text = highlight_matches_html(html_text, keyword, use_regexp)
        web_url = row.get("_slack", "")
        time_display = str(row["time"]).split("#")[-1] if "#" in str(row["time"]) else str(row["time"])
        channel_id = row.get("_channel_id", "")
        channel_html = f"#{row['channel']}"
        if workspace and channel_id:
            ch_app = _slack_app_link(f"https://{workspace}/archives/{channel_id}")
            channel_html = f'<a href="{ch_app}" style="color:#9d4edd;text-decoration:none">#{row["channel"]}</a>'
        links_html = ""
        if web_url:
            app_url = _slack_app_link(web_url)
            links_html = (
                f' &nbsp;<a href="{app_url}" style="font-size:12px;color:#9d4edd;text-decoration:none">🖥 app</a>'
                f' &nbsp;<a href="{web_url}" target="_blank" style="font-size:12px;color:#9d4edd;text-decoration:none">↗ browser</a>'
            )
        st.markdown(
            f"""<div style="border-left:4px solid #9d4edd;border-radius:0 6px 6px 0;
                            padding:14px 20px;margin-top:8px;
                            background:rgba(157,78,221,0.06)">
              <div style="font-size:12px;color:#888;margin-bottom:10px">
                <b>{time_display}</b> &nbsp;·&nbsp; {row['author']} &nbsp;·&nbsp; {channel_html}
                {links_html}
              </div>
              <div style="font-size:18px;line-height:1.8">{html_text}</div>
            </div>""",
            unsafe_allow_html=True,
        )


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


# ── Slack Search tab ─────────────────────────────────────────────────────────

def render_slack_search(db_path: str) -> None:
    st.subheader("Slack Search")

    slack_client = st.session_state.get("slack_client")
    if not slack_client:
        st.info(
            "No Slack credentials configured. Load a `.curl` file in the sidebar "
            "or set `SLACK_TOKEN` / `SLACK_WORKSPACE` in your `.env`."
        )
        return
    if not Path(db_path).exists():
        st.error("No archive database — run a download first.")
        return

    # ── Search form ───────────────────────────────────────────────────────────
    # st.text_input triggers a rerun on Enter (and on blur). We detect "Enter
    # pressed" by comparing the current value against the last submitted query.
    # This avoids st.form which would also fire the sidebar's first button.
    fc1, fc2, fc3 = st.columns([5, 1, 1])
    with fc1:
        query = st.text_input(
            "Query",
            key="slack_search_input",
            placeholder='e.g.  "out of memory"  or  error in:#cost-mgmt-dev after:2024-01-01',
            label_visibility="collapsed",
        )
    with fc2:
        limit = st.selectbox("Results", [25, 50, 100], index=1, label_visibility="collapsed")
    with fc3:
        search_clicked = st.button("Search Slack", type="primary", use_container_width=True)

    # Run search on button click OR when the query text changes (Enter / blur)
    last_submitted = st.session_state.get("_slack_last_submitted", "")
    should_search = bool(query) and (search_clicked or query != last_submitted)

    if should_search:
        st.session_state["_slack_last_submitted"] = query
        with st.spinner("Searching Slack…"):
            last_exc: Exception | None = None
            results = None
            for attempt in range(3):
                try:
                    import time as _time
                    if attempt:
                        _time.sleep(2 * attempt)
                    rw_conn = open_db(Path(db_path))
                    try:
                        results = run_slack_search(rw_conn, slack_client, query, limit=limit)
                    finally:
                        rw_conn.close()
                    last_exc = None
                    break
                except Exception as e:
                    last_exc = e
            if last_exc is not None:
                st.error(f"Search error: {last_exc}")
                st.session_state.pop("slack_search_results", None)
                st.session_state.pop("slack_search_query", None)
                return
        st.session_state["slack_search_results"] = results
        st.session_state["slack_search_query"] = query
        st.rerun()

    results: list[dict] = st.session_state.get("slack_search_results", [])
    query_used: str = st.session_state.get("slack_search_query", "")

    if not results:
        if query_used:
            st.info("No results.")
        return

    # ── Build display DataFrame ───────────────────────────────────────────────
    import pandas as _pd
    df = _pd.DataFrame(results)

    ro_conn = get_messages_conn(db_path)
    uids = extract_uids(df["text"].tolist())
    user_map = build_user_map(ro_conn, uids) if ro_conn else {}
    df["_raw_text"] = df["text"]
    df["text"] = df["text"].apply(lambda t: resolve_mentions(t or "", user_map))
    df["_slack"] = df["permalink"].fillna("")
    # Encode app deep-link into the time column as a URL fragment so LinkColumn can
    # show the time as display text while the cell opens the Slack desktop app.
    df["time"] = df.apply(
        lambda r: (_slack_app_link(r["_slack"]) + "#" + r["time"]) if r["_slack"] else r["time"],
        axis=1,
    )

    display_cols = ["time", "channel", "author", "text"]
    st.caption(f"{len(df)} result(s) for: **{query_used}**  —  new messages cached in local DB")

    event = st.dataframe(
        df[display_cols],
        width="stretch",
        height=400,
        on_select="rerun",
        selection_mode="single-row",
        column_config={
            "time":    st.column_config.LinkColumn("Time",    display_text=r"#(.+)", width="small"),
            "channel": st.column_config.TextColumn("Channel", width="small"),
            "author":  st.column_config.TextColumn("Author",  width="small"),
            "text":    st.column_config.TextColumn("Message", width="large"),
        },
    )

    # ── Detail panel ──────────────────────────────────────────────────────────
    rows = event.selection.rows if event and event.selection else []
    st.caption("Click a row to read the full message below")
    if rows:
        row = df.iloc[rows[0]]
        raw = row.get("_raw_text") or row["text"]
        html_text = resolve_mentions_html(raw, user_map)
        hl_term = extract_highlight_term(query_used)
        html_text = highlight_matches_html(html_text, hl_term)
        web_url = row.get("_slack", "")
        time_display = str(row["time"]).split("#")[-1] if "#" in str(row["time"]) else str(row["time"])
        search_workspace = st.session_state.get("slack_workspace", "")
        channel_id = row.get("channel_id", "")
        channel_html = f"#{row['channel']}"
        if search_workspace and channel_id:
            ch_app = _slack_app_link(f"https://{search_workspace}/archives/{channel_id}")
            channel_html = f'<a href="{ch_app}" style="color:#9d4edd;text-decoration:none">#{row["channel"]}</a>'
        links_html = ""
        if web_url:
            app_url = _slack_app_link(web_url)
            links_html = (
                f' &nbsp;<a href="{app_url}" style="font-size:12px;color:#9d4edd;text-decoration:none">🖥 app</a>'
                f' &nbsp;<a href="{web_url}" target="_blank" style="font-size:12px;color:#9d4edd;text-decoration:none">↗ browser</a>'
            )
        st.markdown(
            f"""<div style="border-left:4px solid #9d4edd;border-radius:0 6px 6px 0;
                            padding:14px 20px;margin-top:8px;
                            background:rgba(157,78,221,0.06)">
              <div style="font-size:12px;color:#888;margin-bottom:10px">
                <b>{time_display}</b> &nbsp;·&nbsp; {row['author']} &nbsp;·&nbsp; {channel_html}
                {links_html}
              </div>
              <div style="font-size:18px;line-height:1.8">{html_text}</div>
            </div>""",
            unsafe_allow_html=True,
        )


# ── Main ──────────────────────────────────────────────────────────────────────

def main() -> None:
    conv_conn = get_conv_conn()
    msg_conn, channel_filter, provider, model, db_path = render_sidebar(conv_conn)

    if msg_conn is None:
        st.info("Set the archive database path in the sidebar and run a download first.")
        st.code(
            'uv run slack-search download --curl "$(cat .curl)" '
            "--channel cost-mgmt-dev --since '3 weeks ago' --no-files",
            language="bash",
        )
        return

    tab_nlq, tab_browse, tab_sql, tab_search = st.tabs([
        "💬 Ask in natural language",
        "📋 Browse messages",
        "🛠 SQL query",
        "🔍 Slack Search",
    ])

    with tab_nlq:
        render_nlq(msg_conn, conv_conn, channel_filter, provider, model)

    with tab_browse:
        render_browse(msg_conn, channel_filter)

    with tab_sql:
        render_sql(msg_conn, channel_filter)

    with tab_search:
        render_slack_search(db_path)


if __name__ == "__main__":
    main()
