import sqlite3
import re
from pathlib import Path
from typing import Optional

from openai import OpenAI

from .search import run_sql

PROMPT_PATH = Path(__file__).parent.parent / "prompts" / "nl_to_sql.md"


def _load_system_prompt() -> str:
    if PROMPT_PATH.exists():
        return PROMPT_PATH.read_text()
    # fallback if prompt file is missing
    from .search import SCHEMA_DESCRIPTION
    return f"You are a SQL expert for a Slack message archive in SQLite.\n\n{SCHEMA_DESCRIPTION}"


def _extract_sql(text: str) -> Optional[str]:
    m = re.search(r"```sql\s*(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1).strip()
    m = re.search(r"(SELECT\s.+?)(?:;|$)", text, re.DOTALL | re.IGNORECASE)
    return m.group(1).strip() if m else None


def ask(
    conn: sqlite3.Connection,
    question: str,
    base_url: str,
    model: str,
    api_key: str = "local",
) -> None:
    from rich.console import Console
    from rich.markdown import Markdown

    console = Console()
    console.print(f"[dim]Querying {model} at {base_url}…[/]")

    client = OpenAI(base_url=base_url, api_key=api_key)
    response = client.chat.completions.create(
        model=model,
        messages=[
            {"role": "system", "content": _load_system_prompt()},
            {"role": "user", "content": question},
        ],
        temperature=0,
    )

    answer = response.choices[0].message.content
    console.print(Markdown(answer))

    sql = _extract_sql(answer)
    if sql:
        console.print("\n[bold cyan]Results:[/]")
        run_sql(conn, sql)
