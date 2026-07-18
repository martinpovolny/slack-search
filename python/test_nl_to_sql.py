#!/usr/bin/env python3
"""
Test the nl_to_sql prompt against the opencode.ai API.

Usage:
    uv run python test_nl_to_sql.py "who posts the most messages?"
    uv run python test_nl_to_sql.py   # runs built-in test questions
"""

import re
import sqlite3
import sys
from pathlib import Path

from dotenv import load_dotenv
from openai import OpenAI

load_dotenv()

import os

OPENCODE_API_KEY = os.getenv("OPENCODE_API_KEY", "").strip()
OPENCODE_BASE_URL = "https://opencode.ai/zen/go/v1"
MODEL = "qwen3.6-plus"

DB_PATH = Path.home() / ".slack-search" / "messages.db"
PROMPT_PATH = Path(__file__).parent / "prompts" / "nl_to_sql.md"

TEST_QUESTIONS = [
    "Who posts the most messages?",
    "Show me the 10 most recent messages with author names.",
    "Which days had the most activity?",
    "Are there any messages with file attachments?",
]


def load_system_prompt() -> str:
    return PROMPT_PATH.read_text()


def ask(question: str, system_prompt: str) -> str:
    client = OpenAI(api_key=OPENCODE_API_KEY, base_url=OPENCODE_BASE_URL)
    resp = client.chat.completions.create(
        model=MODEL,
        temperature=0,
        messages=[
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": question},
        ],
    )
    return resp.choices[0].message.content


def extract_sql(text: str) -> str | None:
    m = re.search(r"```sql\s*(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1).strip()
    m = re.search(r"(SELECT\s.+?)(?:;|$)", text, re.DOTALL | re.IGNORECASE)
    return m.group(1).strip() if m else None


def run_sql(db: sqlite3.Connection, sql: str) -> list[dict]:
    cur = db.execute(sql)
    cols = [d[0] for d in cur.description]
    return [dict(zip(cols, row)) for row in cur.fetchall()]


def print_table(rows: list[dict]) -> None:
    if not rows:
        print("  (no results)")
        return
    cols = list(rows[0].keys())
    widths = {c: max(len(c), max(len(str(r[c])) for r in rows)) for c in cols}
    header = "  " + " | ".join(c.ljust(widths[c]) for c in cols)
    sep = "  " + "-+-".join("-" * widths[c] for c in cols)
    print(header)
    print(sep)
    for row in rows:
        print("  " + " | ".join(str(row[c]).ljust(widths[c]) for c in cols))


def main() -> None:
    if not OPENCODE_API_KEY:
        print("ERROR: OPENCODE_API_KEY not set in .env")
        sys.exit(1)

    if not DB_PATH.exists():
        print(f"ERROR: database not found at {DB_PATH}")
        print("Run: uv run slack-search download --curl \"$(cat .curl)\" --channel cost-mgmt-dev --since '3 weeks ago' --no-files")
        sys.exit(1)

    db = sqlite3.connect(DB_PATH)
    system_prompt = load_system_prompt()

    questions = sys.argv[1:] if len(sys.argv) > 1 else TEST_QUESTIONS

    for question in questions:
        print(f"\n{'='*60}")
        print(f"Q: {question}")
        print(f"{'='*60}")

        answer = ask(question, system_prompt)
        print(answer)

        sql = extract_sql(answer)
        if sql:
            print("\n--- Query results ---")
            try:
                rows = run_sql(db, sql)
                print_table(rows)
                print(f"  ({len(rows)} row(s))")
            except sqlite3.Error as e:
                print(f"  SQL ERROR: {e}")
        else:
            print("\n(no SQL found in response)")


if __name__ == "__main__":
    main()
