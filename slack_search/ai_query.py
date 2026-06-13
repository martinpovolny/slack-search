import json
import os
import sqlite3
import re
from pathlib import Path
from typing import Optional

import httpx
import pandas as pd
from openai import OpenAI

from .search import run_sql

PROMPT_PATH = Path(__file__).parent.parent / "prompts" / "nl_to_sql.md"
RHT_MODELS_FILE = Path(__file__).parent.parent / ".rht_models.json"

MAX_LLM_ROWS = 100
SYNTHESISE_MARKER = "[SYNTHESISE]"


def _load_system_prompt() -> str:
    if PROMPT_PATH.exists():
        return PROMPT_PATH.read_text()
    from .search import SCHEMA_DESCRIPTION
    return f"You are a SQL expert for a Slack message archive in SQLite.\n\n{SCHEMA_DESCRIPTION}"


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
    base_url = template.format(model=model_name)
    api_key = models[model_name]
    api_model_id = f"/data/{model_name}"
    return base_url, api_key, api_model_id


def _http_client() -> httpx.Client | None:
    proxy = os.getenv("HTTPS_PROXY") or os.getenv("ALL_PROXY")
    verify = os.getenv("SSL_NO_VERIFY", "").lower() not in ("1", "true", "yes")
    if proxy:
        return httpx.Client(proxy=proxy, verify=verify)
    if not verify:
        return httpx.Client(verify=False)
    return None


def ask(
    conn: sqlite3.Connection,
    question: str,
    base_url: str,
    model: str,
    api_key: str = "local",
) -> None:
    from rich.console import Console
    from rich.markdown import Markdown
    from rich.table import Table

    console = Console()
    http = _http_client()
    console.print(f"[dim]Querying {model} at {base_url}{'  (via proxy)' if http else ''}…[/]")

    client = OpenAI(base_url=base_url, api_key=api_key, **({"http_client": http} if http else {}))

    # ── Phase 1: NL → SQL ────────────────────────────────────────────────────
    response = client.chat.completions.create(
        model=model,
        messages=[
            {"role": "system", "content": _load_system_prompt()},
            {"role": "user", "content": question},
        ],
        temperature=0,
    )

    answer = response.choices[0].message.content
    synthesise = SYNTHESISE_MARKER in answer
    display_answer = answer.replace(SYNTHESISE_MARKER, "").lstrip("\n").strip()

    console.print(Markdown(display_answer))

    sql = _extract_sql(answer)
    if not sql:
        return

    # ── Execute SQL ──────────────────────────────────────────────────────────
    capped_sql = _cap_sql(sql) if synthesise else sql
    try:
        df = pd.read_sql_query(capped_sql, conn)
    except Exception as e:
        console.print(f"[red]SQL error:[/] {e}")
        return

    if not synthesise:
        run_sql(conn, sql)
        return

    # ── Phase 2: synthesise ──────────────────────────────────────────────────
    console.print(f"\n[dim]Running synthesis over {len(df)} row(s)…[/]")

    header = " | ".join(str(c) for c in df.columns)
    sep = " | ".join("---" for _ in df.columns)
    rows = "\n".join(" | ".join(str(v) for v in row) for row in df.itertuples(index=False))
    results_text = f"{header}\n{sep}\n{rows}"
    if len(df) == MAX_LLM_ROWS:
        results_text += f"\n\n_(results capped at {MAX_LLM_ROWS} rows)_"

    synthesis_response = client.chat.completions.create(
        model=model,
        messages=[
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
                f"Question: {question}"
            )},
        ],
        temperature=0,
    )

    nl_answer = synthesis_response.choices[0].message.content
    console.print("\n[bold cyan]Answer:[/]")
    console.print(Markdown(nl_answer))
    console.print(f"\n[dim](based on {len(df)} row(s), capped at {MAX_LLM_ROWS})[/]")
