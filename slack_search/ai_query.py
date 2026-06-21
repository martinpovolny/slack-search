from __future__ import annotations

import json
import os
import sqlite3
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

import httpx
import pandas as pd
from openai import OpenAI, APIConnectionError, APIStatusError

from .search import run_sql

PROMPT_PATH = Path(__file__).parent.parent / "prompts" / "nl_to_sql.md"
SYNTHESIS_PROMPT_PATH = Path(__file__).parent.parent / "prompts" / "synthesis.md"
RHT_MODELS_FILE = Path(__file__).parent.parent / ".rht_models.json"

MAX_LLM_ROWS = 100
SYNTHESISE_MARKER = "[SYNTHESISE]"


def _load_system_prompt(prompt_path: Path | None = None, conn: sqlite3.Connection | None = None) -> str:
    from datetime import date as _date
    path = prompt_path or PROMPT_PATH
    text = path.read_text() if path.exists() else (
        "You are a SQL expert for a Slack message archive in SQLite.\n\n"
        + __import__("slack_search.search", fromlist=["SCHEMA_DESCRIPTION"]).SCHEMA_DESCRIPTION
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


def _extract_sql(text: str) -> Optional[str]:
    m = re.search(r"```sql\s*(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1).strip()
    m = re.search(r"(SELECT\s.+?)(?:;|$)", text, re.DOTALL | re.IGNORECASE)
    return m.group(1).strip() if m else None


def _cap_sql(sql: str) -> str:
    return f"SELECT * FROM ({sql.rstrip(';')}) _q LIMIT {MAX_LLM_ROWS}"


def load_rht_config(model_name: str) -> tuple[str, str, str]:
    """Return (base_url, api_key, api_model_id) for a named RHT model."""
    if not RHT_MODELS_FILE.exists():
        raise FileNotFoundError(f"{RHT_MODELS_FILE} not found")
    data = json.loads(RHT_MODELS_FILE.read_text())
    template = data.get("url_template", "")
    models = data.get("models", {})
    if model_name not in models:
        available = ", ".join(models.keys())
        raise ValueError(f"Unknown RHT model '{model_name}'. Available: {available}")
    entry = models[model_name]
    base_url = template.format(model=model_name)
    api_key = entry["key"]
    api_model_id = entry.get("api_model_id", model_name)
    return base_url, api_key, api_model_id


def _connection_error(console, base_url: str, exc: Exception) -> None:
    proxy = os.getenv("HTTPS_PROXY") or os.getenv("ALL_PROXY")
    console.print(f"\n[bold red]Connection error[/] — could not reach {base_url}")
    inner = str(exc.__cause__ or exc)
    if "Connection refused" in inner or "Errno 61" in inner:
        if proxy:
            console.print(f"  Proxy [yellow]{proxy}[/] is not reachable. Is the SSH tunnel running?")
            console.print("  Run: [cyan]autossh -N -D 1080 -M 0 -o ServerAliveInterval=30 mpovolny@192.168.77.8[/]")
        else:
            console.print("  The server actively refused the connection. Check the URL and that the service is up.")
    elif "nodename nor servname" in inner or "Name or service not known" in inner:
        if proxy:
            console.print(f"  DNS failed — the proxy [yellow]{proxy}[/] may be down or not routing DNS.")
        else:
            console.print("  Hostname could not be resolved. Set ALL_PROXY or check your network.")
    elif "SSL" in inner or "certificate" in inner.lower():
        console.print("  TLS error. Try setting [cyan]SSL_NO_VERIFY=true[/] in .env.")
    else:
        console.print(f"  {inner}")


def _http_client() -> httpx.Client | None:
    proxy = os.getenv("HTTPS_PROXY") or os.getenv("ALL_PROXY")
    verify = os.getenv("SSL_NO_VERIFY", "").lower() not in ("1", "true", "yes")
    if proxy:
        return httpx.Client(proxy=proxy, verify=verify)
    if not verify:
        return httpx.Client(verify=False)
    return None


# ── Core structured result ────────────────────────────────────────────────────

@dataclass
class QueryResult:
    question: str
    raw_response: str | None = None
    sql: str | None = None
    mode: str = "table"          # "table" | "synthesise" | "error"
    df: pd.DataFrame | None = None
    nl_answer: str | None = None
    error: str | None = None


def run_query(
    conn: sqlite3.Connection,
    question: str,
    client: OpenAI,
    model: str,
    prompt_path: Path | None = None,
) -> QueryResult:
    """Run the full NL→SQL→(synthesise) pipeline and return structured result."""
    result = QueryResult(question=question)

    # ── Phase 1: NL → SQL ────────────────────────────────────────────────────
    try:
        response = client.chat.completions.create(
            model=model,
            messages=[
                {"role": "system", "content": _load_system_prompt(prompt_path, conn)},
                {"role": "user", "content": question},
            ],
            temperature=0,
        )
    except (APIConnectionError, APIStatusError) as e:
        result.mode = "error"
        result.error = str(e)
        return result
    except Exception as e:
        result.mode = "error"
        result.error = f"API error: {e}"
        return result

    if not hasattr(response, "choices") or not response.choices:
        result.mode = "error"
        result.error = f"Unexpected API response type: {type(response).__name__}"
        return result

    raw = response.choices[0].message.content
    result.raw_response = raw
    synthesise = SYNTHESISE_MARKER in raw
    result.mode = "synthesise" if synthesise else "table"

    sql = _extract_sql(raw)
    result.sql = sql
    if not sql:
        return result

    # ── Execute SQL ──────────────────────────────────────────────────────────
    try:
        capped = _cap_sql(sql) if synthesise else sql
        result.df = pd.read_sql_query(capped, conn)
    except Exception as e:
        result.error = f"SQL error: {e}"
        return result

    if not synthesise:
        return result

    # ── Phase 2: synthesise ──────────────────────────────────────────────────
    rows_text = _df_to_markdown(result.df, conn)
    from datetime import date as _date
    today_str = _date.today().strftime("%A, %Y-%m-%d")
    synthesis_system = SYNTHESIS_PROMPT_PATH.read_text().replace("{today}", today_str)

    try:
        synthesis_response = client.chat.completions.create(
            model=model,
            messages=[
                {"role": "system", "content": synthesis_system},
                {"role": "user", "content": (
                    f"SQL query that produced these results:\n```sql\n{sql}\n```\n\n"
                    f"Query results:\n\n{rows_text}\n\n"
                    f"Question: {question}"
                )},
            ],
            temperature=0,
        )
        if hasattr(synthesis_response, "choices") and synthesis_response.choices:
            result.nl_answer = synthesis_response.choices[0].message.content
        else:
            result.error = f"Synthesis: unexpected response type: {type(synthesis_response).__name__}"
    except (APIConnectionError, APIStatusError) as e:
        result.error = f"Synthesis error: {e}"
    except Exception as e:
        result.error = f"Synthesis error: {e}"

    return result


def _df_to_markdown(df: pd.DataFrame, conn: sqlite3.Connection | None = None) -> str:
    if df.empty:
        return "(no rows returned)"
    # Resolve <@U…> mentions in text columns before sending to the LLM
    if conn is not None:
        from .slack_format import build_user_map, extract_uids, resolve_mentions
        text_cols = [c for c in df.columns if "text" in c.lower() or "message" in c.lower()]
        if text_cols:
            all_texts = [str(v) for col in text_cols for v in df[col] if v]
            uids = extract_uids(all_texts)
            user_map = build_user_map(conn, uids)
            for col in text_cols:
                df = df.copy()
                df[col] = df[col].apply(lambda v: resolve_mentions(str(v), user_map) if v else v)
    header = " | ".join(str(c) for c in df.columns)
    sep = " | ".join("---" for _ in df.columns)
    rows = "\n".join(" | ".join(str(v) for v in row) for row in df.itertuples(index=False))
    note = f"\n\n_(results capped at {MAX_LLM_ROWS} rows)_" if len(df) == MAX_LLM_ROWS else ""
    return f"{header}\n{sep}\n{rows}{note}"


# ── CLI-facing ask() — calls run_query and prints ────────────────────────────

def ask(
    conn: sqlite3.Connection,
    question: str,
    base_url: str,
    model: str,
    api_key: str = "local",
    prompt_path: Path | None = None,
) -> None:
    from rich.console import Console
    from rich.markdown import Markdown

    console = Console()
    http = _http_client()
    console.print(f"[dim]Querying {model} at {base_url}{'  (via proxy)' if http else ''}…[/]")

    client = OpenAI(base_url=base_url, api_key=api_key, **({"http_client": http} if http else {}))
    result = run_query(conn, question, client, model, prompt_path)

    if result.error and result.mode == "error":
        # Distinguish connection errors from SQL/synthesis errors
        if "Connection" in result.error or "APIConnection" in result.error:
            _connection_error(console, base_url, Exception(result.error))
        else:
            console.print(f"[red]Error:[/] {result.error}")
        return

    display = (result.raw_response or "").replace(SYNTHESISE_MARKER, "").lstrip("\n").strip()
    console.print(Markdown(display))

    if result.error:
        console.print(f"[red]{result.error}[/]")
        return

    if result.sql and result.mode == "table" and result.df is not None:
        run_sql(conn, result.sql)

    if result.nl_answer:
        console.print("\n[bold cyan]Answer:[/]")
        console.print(Markdown(result.nl_answer))
        console.print(f"\n[dim](based on {len(result.df)} row(s), capped at {MAX_LLM_ROWS})[/]")
