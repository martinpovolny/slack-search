import sqlite3
from rich.console import Console
from rich.table import Table

console = Console()

SCHEMA_DESCRIPTION = """\
Tables and columns available for SQL queries:

messages(ts TEXT, channel_id TEXT, user_id TEXT, username TEXT, text TEXT,
         timestamp REAL, thread_ts TEXT, reply_count INTEGER)
  - ts: Slack message timestamp/id (e.g. '1718000000.123456')
  - timestamp: Unix epoch float (same value as ts, for range comparisons)
  - text: message body

channels(id TEXT, name TEXT, subscribed INTEGER)
  - subscribed: 1 if the channel was explicitly downloaded, 0 if only seen via live-search

users(id TEXT, name TEXT, real_name TEXT, display_name TEXT)

files(id TEXT, ts TEXT, channel_id TEXT, name TEXT, mimetype TEXT, url TEXT, local_path TEXT)

Useful joins:
  messages m JOIN users u ON m.user_id = u.id
  messages m JOIN channels c ON m.channel_id = c.id
  messages m JOIN files f ON f.ts = m.ts AND f.channel_id = m.channel_id

datetime(timestamp, 'unixepoch') converts timestamp to a readable string.
"""


def run_sql(conn: sqlite3.Connection, sql: str) -> None:
    try:
        cur = conn.execute(sql)
        rows = cur.fetchall()
    except sqlite3.Error as e:
        console.print(f"[red]SQL error:[/] {e}")
        return

    if not rows:
        console.print("[dim]No results.[/]")
        return

    cols = [d[0] for d in cur.description]
    table = Table(*cols, show_header=True, header_style="bold cyan")
    for row in rows:
        table.add_row(*[str(v) if v is not None else "" for v in row])
    console.print(table)


def show_schema(conn: sqlite3.Connection) -> None:
    console.print(SCHEMA_DESCRIPTION)
