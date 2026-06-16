import os
import sys
from pathlib import Path
from typing import Optional

import click
from dotenv import load_dotenv
from rich.console import Console

load_dotenv()

from .database import open_db
from .downloader import download, _parse_since
from .search import run_sql, show_schema
from .ai_query import ask, load_rht_config
from .eval import run_eval, save_results, print_summary, TESTS_DIR
from .curl_parser import parse_curl
from .grep import grep_messages
from .slack_search_api import run_slack_search, extract_highlight_term

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
@click.option("--curl", "curl_command", envvar="SLACK_CURL", default=None, metavar="CURL",
              help="'Copy as cURL' command from Chrome DevTools to auto-extract credentials")
@click.option("--token", envvar="SLACK_TOKEN", default=None, help="Slack token")
@click.option("--cookie", envvar="SLACK_COOKIE", default=None, help="Value of the 'd' cookie (xoxc- only)")
@click.option("--workspace", envvar="SLACK_WORKSPACE", default=None, help="Workspace hostname")
@click.option("--files-dir", default=str(DEFAULT_FILES_DIR), show_default=True, help="Directory for file attachments")
@click.option("--no-files", is_flag=True, default=False, help="Skip downloading file attachments")
@click.option("--no-threads", is_flag=True, default=False, help="Skip fetching thread replies")
@click.pass_context
def refresh(
    ctx: click.Context,
    curl_command: Optional[str],
    token: Optional[str],
    cookie: Optional[str],
    workspace: Optional[str],
    files_dir: str,
    no_files: bool,
    no_threads: bool,
) -> None:
    """Refresh all known channels: fetch new messages since the last download.

    Reads the list of channels from the database and runs an incremental
    download for each one, resuming from where the last run left off.
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
        raw_cookies = creds.raw_cookies

    if not token:
        console.print("[red]Error:[/] No token found. Pass --token, set SLACK_TOKEN, or use --curl.")
        raise SystemExit(1)

    conn = ctx.obj["db"]
    channels = conn.execute("SELECT id, name FROM channels ORDER BY name").fetchall()
    if not channels:
        console.print("[yellow]No channels in database. Run 'download' first to add channels.[/]")
        return

    dest = None if no_files else Path(files_dir)
    total_new = 0
    for ch in channels:
        channel_id, channel_name = ch["id"], ch["name"]
        console.print(f"\n[bold]#{channel_name}[/] ({channel_id})")
        try:
            count = download(
                conn=conn,
                token=token,
                cookie=cookie,
                workspace=workspace,
                raw_cookies=raw_cookies,
                channel=channel_id,
                since=None,
                files_dir=dest,
                fetch_threads=not no_threads,
            )
            console.print(f"  [green]✓[/] {count} new message(s)")
            total_new += count
        except Exception as e:
            console.print(f"  [red]✗ Error:[/] {e}")

    console.print(f"\n[green]Done.[/] {total_new} new message(s) across {len(channels)} channel(s).")


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


@cli.command()
@click.option("--rht-model", default=None, help="RHT model name for query generation")
@click.option("--llm-url", envvar="LLM_BASE_URL", default=DEFAULT_LLM_URL, help="LLM base URL")
@click.option("--llm-model", envvar="LLM_MODEL", default=DEFAULT_LLM_MODEL, help="Model name")
@click.option("--llm-api-key", envvar="LLM_API_KEY", default="local", help="API key")
@click.option("--test-ids", default=None, help="Comma-separated list of test IDs to run (default: all)")
@click.option("--no-judge", is_flag=True, default=False, help="Skip LLM judge, only run SQL checks")
@click.option("--judge-opencode", is_flag=True, default=False, help="Use OpenCode.ai as the judge (default: same model)")
@click.option("--judge-model", "judge_model_name", default=None, help="OpenCode model for judging (default: qwen3.6-plus)")
@click.option("--prompt", default=None, type=click.Path(exists=True), help="Custom system prompt file")
@click.pass_context
def eval_cmd(
    ctx: click.Context,
    rht_model: str,
    llm_url: str,
    llm_model: str,
    llm_api_key: str,
    test_ids: str,
    no_judge: bool,
    judge_opencode: bool,
    judge_model_name: str,
    prompt: str,
) -> None:
    """Run NL→SQL evaluation suite against a set of test cases."""
    from pathlib import Path as _Path
    from openai import OpenAI as _OpenAI
    from .ai_query import _http_client, PROMPT_PATH

    if rht_model:
        llm_url, llm_api_key, llm_model = load_rht_config(rht_model)

    http = _http_client()
    client = _OpenAI(base_url=llm_url, api_key=llm_api_key, **({"http_client": http} if http else {}))

    # Judge client — OpenCode.ai by default when --judge-opencode is set
    if no_judge:
        judge_client, judge_model = None, None
    elif judge_opencode:
        opencode_key = os.getenv("OPENCODE_API_KEY", "")
        if not opencode_key:
            raise click.UsageError("OPENCODE_API_KEY not set in .env")
        judge_client = _OpenAI(
            api_key=opencode_key,
            base_url="https://opencode.ai/zen/go/v1",
        )
        judge_model = judge_model_name or "qwen3.6-plus"
        print(f"Judge: OpenCode.ai / {judge_model}")
    else:
        judge_client = client
        judge_model = llm_model

    conn = ctx.obj["db"]
    ids = [t.strip() for t in test_ids.split(",")] if test_ids else None
    prompt_path = _Path(prompt) if prompt else PROMPT_PATH

    results = run_eval(
        conn=conn,
        client=client,
        model=llm_model,
        test_ids=ids,
        judge_client=judge_client,
        judge_model=judge_model,
        prompt_path=prompt_path,
    )

    print_summary(results)
    out = save_results(results, prompt_path)
    print(f"\nResults saved to: {out}")


@cli.command(name="grep")
@click.option("-F", "--string", "fixed_string", default=None, metavar="TEXT",
              help="Search for literal string (case-insensitive)")
@click.option("-E", "--regexp", "pattern", default=None, metavar="PATTERN",
              help="Search for regular expression (case-insensitive)")
@click.option("-c", "--channel", "channels", multiple=True, metavar="CHANNEL",
              help="Limit to channel name or ID (repeat for multiple, default: all)")
@click.option("--since", default=None, metavar="DATE",
              help="Only messages after this date (e.g. '2024-01-01', '3 weeks ago')")
@click.option("--until", default=None, metavar="DATE",
              help="Only messages before this date")
@click.option("-p", "--person", default=None, metavar="NAME",
              help="Filter by sender name (partial match)")
@click.option("-n", "--limit", default=200, show_default=True,
              help="Maximum number of results")
@click.option("-P", "--pager", is_flag=True, default=False,
              help="Page output through 'less -R' with colours preserved")
@click.pass_context
def grep_cmd(
    ctx: click.Context,
    fixed_string: Optional[str],
    pattern: Optional[str],
    channels: tuple,
    since: Optional[str],
    until: Optional[str],
    person: Optional[str],
    limit: int,
    pager: bool,
) -> None:
    """Search messages by string (-F) or regular expression (-E).

    \b
    Examples:
      slack-search grep -F "out of memory"
      slack-search grep -E "error|warning" --channel cost-mgmt-dev --since "last week"
      slack-search grep -F "budget" --person Martin --since 2024-01-01 --until 2024-02-01
      slack-search grep -E "OCP|provider_uuid" -P   # page with colours
    """
    import re as _re
    from rich.text import Text
    from rich.console import Console as _Console

    if not fixed_string and not pattern:
        raise click.UsageError("Provide -F/--string or -E/--regexp.")
    if fixed_string and pattern:
        raise click.UsageError("-F and -E are mutually exclusive.")

    conn = ctx.obj["db"]

    try:
        results = grep_messages(
            conn,
            fixed_string=fixed_string,
            pattern=pattern,
            channels=channels,
            since=_parse_since(since) if since else None,
            until=_parse_since(until) if until else None,
            person=person,
            limit=limit,
        )
    except ValueError as e:
        console.print(f"[red]Error:[/] {e}")
        raise SystemExit(1)

    if not results:
        console.print("[yellow]No matches found.[/]")
        return

    # Build a user-id → display name map for mention resolution
    mention_re = _re.compile(r'<@([A-Z0-9]+)(?:\|[^>]*)?>')
    all_uids = {
        m.group(1)
        for row in results
        for m in mention_re.finditer(row.get("text") or "")
    }
    user_map: dict[str, str] = {}
    if all_uids:
        placeholders = ",".join("?" * len(all_uids))
        rows = conn.execute(
            f"SELECT id, COALESCE(real_name, display_name, name, id) AS name "
            f"FROM users WHERE id IN ({placeholders})",
            list(all_uids),
        ).fetchall()
        user_map = {r[0]: r[1] for r in rows}

    # Build highlight regex
    if fixed_string:
        hl_re = _re.compile(_re.escape(fixed_string), _re.IGNORECASE)
    else:
        hl_re = _re.compile(pattern, _re.IGNORECASE)

    def _highlight_segment(segment: str) -> Text:
        """Apply search highlight to a plain-text segment."""
        t = Text()
        last = 0
        for m in hl_re.finditer(segment):
            t.append(segment[last:m.start()])
            t.append(segment[m.start():m.end()], style="bold yellow on dark_red")
            last = m.end()
        t.append(segment[last:])
        return t

    def render_message(text: str) -> Text:
        """Resolve <@U…> mentions (bold magenta) and highlight search matches."""
        result = Text()
        last = 0
        for m in mention_re.finditer(text):
            result.append_text(_highlight_segment(text[last:m.start()]))
            uid = m.group(1)
            name = user_map.get(uid, uid)
            result.append(f"@{name}", style="bold magenta")
            last = m.end()
        result.append_text(_highlight_segment(text[last:]))
        return result

    def render(out: _Console) -> None:
        out.print(f"[dim]{len(results)} match(es)[/]\n")
        for row in results:
            thread_mark = " [dim]↳[/]" if row["thread_ts"] and row["thread_ts"] != row["ts"] else ""
            out.print(
                f"[cyan]{row['time']}[/]  [green]{row['channel']}[/]  [bold]{row['author']}[/]{thread_mark}"
            )
            out.print(render_message(row["text"] or ""))
            out.print()

    if pager:
        with console.pager(styles=True):
            render(console)
    else:
        render(console)


@cli.command(name="live-search")
@click.argument("query")
@click.option("--curl", "curl_command", envvar="SLACK_CURL", default=None, metavar="CURL",
              help="'Copy as cURL' command from Chrome DevTools")
@click.option("--token", envvar="SLACK_TOKEN", default=None, help="Slack token")
@click.option("--cookie", envvar="SLACK_COOKIE", default=None, help="Value of the 'd' cookie (xoxc- only)")
@click.option("--workspace", envvar="SLACK_WORKSPACE", default=None, help="Workspace hostname")
@click.option("-n", "--limit", default=50, show_default=True, help="Maximum number of results")
@click.option("-P", "--pager", is_flag=True, default=False, help="Page output with colours preserved")
@click.pass_context
def live_search_cmd(
    ctx: click.Context,
    query: str,
    curl_command: Optional[str],
    token: Optional[str],
    cookie: Optional[str],
    workspace: Optional[str],
    limit: int,
    pager: bool,
) -> None:
    """Search Slack directly using the built-in search API and cache results locally.

    \b
    Supports Slack search operators:
      in:#channel  from:@user  before:YYYY-MM-DD  after:YYYY-MM-DD  "exact phrase"
    \b
    Examples:
      slack-search live-search "out of memory"
      slack-search live-search 'error in:#cost-mgmt-dev after:2024-01-01'
      slack-search live-search '"budget cut"' -n 20
    """
    import re as _re
    from rich.text import Text
    from rich.console import Console as _Console

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
        raw_cookies = creds.raw_cookies

    if not token:
        console.print("[red]Error:[/] No token. Pass --token, set SLACK_TOKEN, or use --curl.")
        raise SystemExit(1)

    from .slack_client import SlackClient
    client = SlackClient(token=token, cookie=cookie, workspace=workspace, raw_cookies=raw_cookies)

    conn = ctx.obj["db"]
    console.print(f"[dim]Searching Slack for:[/] [bold]{query}[/]")
    try:
        results = run_slack_search(conn, client, query, limit=limit)
    except Exception as e:
        console.print(f"[red]Search error:[/] {e}")
        raise SystemExit(1)

    if not results:
        console.print("[yellow]No results.[/]")
        return

    hl_term = extract_highlight_term(query)
    hl_re = _re.compile(_re.escape(hl_term), _re.IGNORECASE) if hl_term else None

    mention_re = _re.compile(r'<@([A-Z0-9]+)(?:\|[^>]*)?>')
    all_uids = {m.group(1) for row in results for m in mention_re.finditer(row.get("text") or "")}
    user_map: dict[str, str] = {}
    if all_uids:
        placeholders = ",".join("?" * len(all_uids))
        rows = conn.execute(
            f"SELECT id, COALESCE(real_name, display_name, name, id) AS name "
            f"FROM users WHERE id IN ({placeholders})",
            list(all_uids),
        ).fetchall()
        user_map = {r[0]: r[1] for r in rows}

    def _highlight(segment: str) -> Text:
        if not hl_re:
            return Text(segment)
        t = Text()
        last = 0
        for m in hl_re.finditer(segment):
            t.append(segment[last:m.start()])
            t.append(segment[m.start():m.end()], style="bold yellow on dark_red")
            last = m.end()
        t.append(segment[last:])
        return t

    def _render_text(text: str) -> Text:
        result = Text()
        last = 0
        for m in mention_re.finditer(text):
            result.append_text(_highlight(text[last:m.start()]))
            result.append(f"@{user_map.get(m.group(1), m.group(1))}", style="bold magenta")
            last = m.end()
        result.append_text(_highlight(text[last:]))
        return result

    def render(out: _Console) -> None:
        out.print(f"[dim]{len(results)} result(s)[/]\n")
        for row in results:
            out.print(
                f"[cyan]{row['time']}[/]  [green]{row['channel']}[/]  [bold]{row['author']}[/]"
            )
            out.print(_render_text(row["text"] or ""))
            if row.get("permalink"):
                out.print(f"[dim]{row['permalink']}[/]")
            out.print()

    if pager:
        with console.pager(styles=True):
            render(console)
    else:
        render(console)


# Aliases
cli.add_command(download_cmd, name="download")
cli.add_command(eval_cmd, name="eval")
cli.add_command(refresh, name="refresh")
cli.add_command(grep_cmd, name="grep")
