import os
import sys
from pathlib import Path
from typing import Optional

import click
from rich.console import Console

from .database import open_db
from .downloader import download
from .search import run_sql, show_schema
from .ai_query import ask, load_rht_config
from .curl_parser import parse_curl

console = Console()

DEFAULT_DB = Path.home() / ".slack-search" / "messages.db"
DEFAULT_FILES_DIR = Path.home() / ".slack-search" / "files"
DEFAULT_LLM_URL = "http://localhost:11434/v1"
DEFAULT_LLM_MODEL = "qwen2.5-coder:7b"


@click.group()
@click.option("--db", default=str(DEFAULT_DB), show_default=True, help="SQLite database path")
@click.pass_context
def cli(ctx: click.Context, db: str) -> None:
    """Slack message archive and natural-language search tool."""
    ctx.ensure_object(dict)
    db_path = Path(db)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    ctx.obj["db"] = open_db(db_path)


TOKEN_HELP = """
[bold]How to get a Slack token[/bold]

[yellow]Option A — User token (xoxp-…)[/yellow]  reads any channel you can see in the UI.
  1. Go to [link]https://api.slack.com/apps[/link] → Create New App → From scratch
  2. OAuth & Permissions → User Token Scopes → add:
       channels:history  channels:read  groups:history  groups:read
       im:history  mpim:history  files:read  users:read
  3. Install App to Workspace → copy the [bold]User OAuth Token[/bold]

[yellow]Option B — Bot token (xoxb-…)[/yellow]  must be invited to each channel first.
  1. Same as above but add [bold]Bot Token Scopes[/bold] instead
  2. After install, invite the bot: [dim]/invite @your-app-name[/dim] in the channel

[yellow]Option C — extract token from your active browser session (quickest)[/yellow]
  1. Open Slack in Chrome (browser, not the desktop app)
  2. Open DevTools: [bold]Cmd+Option+I[/bold] (Mac) or [bold]F12[/bold] (Windows/Linux)
  3. Go to the [bold]Network[/bold] tab, click any channel to trigger a request
  4. Find a request to [bold]…/api/conversations.history[/bold] and click it
  5. Open the [bold]Payload[/bold] tab (not Headers) — look for the form-data field:
       [bold cyan]token: xoxc-…[/bold cyan]
  6. Also open the [bold]Headers[/bold] tab → scroll to [bold]Request Headers[/bold] → find the [bold cyan]Cookie[/bold] header
     and copy the value of the [bold cyan]d=[/bold cyan] field (starts with [bold]xoxd-[/bold])

  xoxc- tokens require the [bold]d cookie[/bold] to authenticate — you need both.
  Pass them to the download command:
    [bold]uv run slack-search download --channel general \\
      --token xoxc-… --cookie xoxd-… --workspace your-org.enterprise.slack.com[/bold]

  [dim]Note: xoxc- tokens expire when you log out of the browser session.[/dim]

[bold]Persist credentials so you don't retype them:[/bold]
  export SLACK_TOKEN=xoxc-…
  export SLACK_COOKIE=xoxd-…
  export SLACK_WORKSPACE=your-org.enterprise.slack.com   [dim]# omit for plain slack.com[/dim]
"""


@cli.command()
@click.option("--curl", "curl_command", envvar="SLACK_CURL", default=None, metavar="CURL",
              help="Paste a full 'Copy as cURL' command from Chrome DevTools to auto-extract all credentials")
@click.option("--token", envvar="SLACK_TOKEN", default=None, help="Slack token — xoxp-/xoxb-/xoxc- (or set SLACK_TOKEN)")
@click.option("--cookie", envvar="SLACK_COOKIE", default=None, help="Value of the 'd' cookie — required for xoxc- tokens (or set SLACK_COOKIE)")
@click.option("--workspace", envvar="SLACK_WORKSPACE", default=None, help="Workspace hostname, e.g. myorg.enterprise.slack.com (or set SLACK_WORKSPACE)")
@click.option("--channel", default=None, help="Channel name (e.g. general) or ID")
@click.option(
    "--since",
    default=None,
    metavar="DATE",
    help="Only fetch messages after this date/timestamp (default: resume from last run)",
)
@click.option(
    "--check-missing",
    is_flag=True,
    default=False,
    help="Scan for and fill gaps in the existing archive",
)
@click.option(
    "--files-dir",
    default=str(DEFAULT_FILES_DIR),
    show_default=True,
    help="Directory to save file attachments",
)
@click.option("--no-files", is_flag=True, default=False, help="Skip downloading file attachments")
@click.option("--no-threads", is_flag=True, default=False, help="Skip fetching thread replies")
@click.pass_context
def download_cmd(
    ctx: click.Context,
    curl_command: Optional[str],
    token: Optional[str],
    cookie: Optional[str],
    workspace: Optional[str],
    channel: Optional[str],
    since: Optional[str],
    check_missing: bool,
    files_dir: str,
    no_files: bool,
    no_threads: bool,
) -> None:
    """Download messages from a Slack channel into the local database.

    \b
    --since accepts a human-readable date ('2024-01-01', 'yesterday', '3 days ago')
    or a raw Unix/Slack timestamp. Omit to resume from the last download.

    \b
    For browser-extracted xoxc- tokens you must also pass --cookie (the 'd' cookie
    value starting with xoxd-) and --workspace (e.g. myorg.enterprise.slack.com).
    """
    raw_cookies: Optional[str] = None
    if curl_command:
        try:
            creds = parse_curl(curl_command)
        except ValueError as e:
            console.print(f"[red]Could not parse curl command:[/] {e}")
            raise SystemExit(1)
        token = token or creds.token
        cookie = cookie or creds.cookie
        workspace = workspace or creds.workspace
        channel = channel or creds.channel_id
        raw_cookies = creds.raw_cookies
        channel_id_hint = creds.channel_id  # always keep for Enterprise name resolution
    else:
        channel_id_hint = None
        console.print(
            f"[dim]Parsed from curl: workspace=[bold]{workspace}[/bold]"
            f"  channel=[bold]{channel}[/bold]"
            f"  token={token[:12]}…"
            f"  cookie={'yes' if raw_cookies else 'no'}[/dim]"
        )

    if not token:
        console.print(TOKEN_HELP)
        raise SystemExit(1)
    if token.startswith("xoxc-") and not cookie:
        console.print(
            "[red]Error:[/] xoxc- tokens require the browser session cookie.\n"
            "Pass [bold]--cookie xoxd-…[/bold] (the value of the 'd=' cookie from DevTools Headers tab)\n"
            "or set [bold]SLACK_COOKIE[/bold] in your environment.\n\n"
            "Run [bold]uv run slack-search download[/bold] (no args) for full instructions."
        )
        raise SystemExit(1)
    if not channel:
        console.print("[red]Error:[/] --channel is required.\n")
        console.print("Example:  uv run slack-search download --channel general")
        raise SystemExit(1)

    conn = ctx.obj["db"]
    dest = None if no_files else Path(files_dir)
    count = download(
        conn=conn,
        token=token,
        cookie=cookie,
        workspace=workspace,
        raw_cookies=raw_cookies,
        channel=channel,
        since=since,
        files_dir=dest,
        check_missing=check_missing,
        fetch_threads=not no_threads,
        channel_id_hint=channel_id_hint,
    )
    console.print(f"[green]Done.[/] {count} new message(s) stored.")


@cli.command()
@click.argument("sql_query")
@click.pass_context
def search(ctx: click.Context, sql_query: str) -> None:
    """Run a raw SQL query against the message database."""
    conn = ctx.obj["db"]
    run_sql(conn, sql_query)


@cli.command()
@click.pass_context
def schema(ctx: click.Context) -> None:
    """Show the database schema and available columns."""
    show_schema(ctx.obj["db"])


@cli.command()
@click.argument("question")
@click.option(
    "--llm-url",
    envvar="LLM_BASE_URL",
    default=DEFAULT_LLM_URL,
    show_default=True,
    help="OpenAI-compatible API base URL",
)
@click.option(
    "--llm-model",
    envvar="LLM_MODEL",
    default=DEFAULT_LLM_MODEL,
    show_default=True,
    help="Model name to use",
)
@click.option(
    "--llm-api-key",
    envvar="LLM_API_KEY",
    default="local",
    help="API key (use 'local' for Ollama)",
)
@click.option(
    "--rht-model",
    default=None,
    help="Use a RHT models.corp model by name (reads URL/key from .rht_models.json).",
)
@click.pass_context
def nlq(
    ctx: click.Context,
    question: str,
    llm_url: str,
    llm_model: str,
    llm_api_key: str,
    rht_model: str,
) -> None:
    """Ask a natural language question about your Slack archive."""
    conn = ctx.obj["db"]
    if rht_model:
        llm_url, llm_api_key, llm_model = load_rht_config(rht_model)
    ask(conn, question, base_url=llm_url, model=llm_model, api_key=llm_api_key)


# Alias
cli.add_command(download_cmd, name="download")
