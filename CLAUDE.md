# slack-search

CLI + Streamlit tool for archiving Slack channels locally and querying them with SQL or natural language.

## Stack

- **Python 3.11+**, managed with `uv`
- **SQLite** — local message store at `~/.slack-search/messages.db`
- **Custom HTTP client** (`slack_search/slack_client.py`) — uses `requests` with POST + form-body token; the Slack SDK is NOT used because Enterprise Slack rejects `xoxc-` tokens sent in the `Authorization: Bearer` header
- **OpenAI-compatible API** for NL→SQL (default: Ollama at `localhost:11434/v1`)
- **Streamlit** web UI (`app.py`) — same style as the dictpert project

## Running

```bash
# Download a channel (Enterprise Slack via browser curl)
uv run slack-search download --curl "$(cat .curl)" --channel cost-mgmt-dev --since "3 weeks ago" --no-files

# Download (plain Slack with xoxp- token)
SLACK_TOKEN=xoxp-... uv run slack-search download --channel general --since "2024-01-01"

# Raw SQL search
uv run slack-search search "SELECT u.real_name, count(*) FROM messages m JOIN users u ON m.user_id=u.id GROUP BY u.id ORDER BY 2 DESC LIMIT 10"

# Natural language query
LLM_BASE_URL=https://api.opencode.ai/v1 LLM_MODEL=... uv run slack-search nlq "who sends the most messages?"

# Web UI
uv run streamlit run app.py
```

## Testing after every edit

After any code change, run the relevant check before considering the task done:

1. **Syntax / import check** (always):
   ```bash
   uv run slack-search --help
   ```

2. **Curl parsing** (after changes to `curl_parser.py`):
   ```bash
   uv run python3 -c "from slack_search.curl_parser import parse_curl; c=parse_curl(open('.curl').read()); print(c.token[:12], c.workspace, c.channel_id, bool(c.raw_cookies))"
   ```

3. **Live download** (after changes to `downloader.py` or `slack_client.py`):
   ```bash
   uv run slack-search download --curl "$(cat .curl)" --channel cost-mgmt-dev --since "3 weeks ago" --no-files --no-threads
   ```

4. **SQL search** (after changes to `search.py` or `database.py`):
   ```bash
   uv run slack-search search "SELECT count(*) FROM messages"
   ```

## Key design decisions

- **Enterprise Slack auth**: `xoxc-` browser tokens require ALL session cookies (not just `d=xoxd-…`) sent as a `Cookie` header on every POST request. The full cookie string is extracted from `--curl`.
- **Channel resolution**: `conversations.list` is banned in Enterprise Slack. Resolution order: (1) direct channel ID, (2) DB cache from a previous run, (3) `hint_id` from the curl payload, (4) `conversations.list` with a friendly error if restricted.
- **Rate limiting**: hard cap of 1 req/s enforced in `SlackClient._throttle()`; retries on HTTP 429 with `Retry-After`.
- **Cursor stored**: `download_state` table tracks `latest_ts` / `oldest_ts` per channel so reruns are incremental by default.

## File layout

```
slack_search/
  cli.py          — click CLI entry point
  curl_parser.py  — parses Chrome DevTools "Copy as cURL" output
  slack_client.py — raw HTTP Slack API client (POST form-body auth)
  downloader.py   — channel resolution, pagination, thread fetching
  database.py     — SQLite schema and CRUD
  search.py       — SQL runner + schema documentation
  ai_query.py     — NL → SQL via OpenAI-compatible API
app.py            — Streamlit web UI
.curl             — saved curl command for credentials (gitignored)
```

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `SLACK_TOKEN` | — | Slack token (xoxp-/xoxb-/xoxc-) |
| `SLACK_COOKIE` | — | `d` cookie value (xoxc- only) |
| `SLACK_WORKSPACE` | — | e.g. `myorg.enterprise.slack.com` |
| `LLM_BASE_URL` | `http://localhost:11434/v1` | OpenAI-compatible LLM endpoint |
| `LLM_MODEL` | `qwen2.5-coder:7b` | Model name |
| `LLM_API_KEY` | `local` | API key (`local` for Ollama) |
