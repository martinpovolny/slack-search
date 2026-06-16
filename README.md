# slack-search

Download Slack channel archives and search them with SQL or natural language.

## Requirements

- Python 3.11+
- [uv](https://docs.astral.sh/uv/) — `pip install uv`

## Loading data from Slack

### Enterprise Slack (xoxc- browser token)

1. Open Slack in Chrome, open DevTools → Network, find any `conversations.history` request, right-click → **Copy as cURL**, paste into `.curl`:

```bash
# Save the copied curl to a file
cat > .curl   # paste, then Ctrl-D
```

2. Download a channel:

```bash
uv run slack-search download --curl "$(cat .curl)" --channel cost-mgmt-dev --since "3 weeks ago" --no-files
```

Reruns are incremental — only new messages are fetched.

### Refreshing all channels at once

Once you have downloaded one or more channels, you can update all of them in one go:

```bash
uv run slack-search refresh --curl "$(cat .curl)" --no-files
```

This reads every channel stored in the database and fetches new messages since the last run for each one. Accepts the same credential options as `download`.

### Standard Slack (xoxp- / xoxb- token)

```bash
SLACK_TOKEN=xoxp-... uv run slack-search download --channel general --since "2024-01-01"
```

## Running the web UI

```bash
uv run streamlit run app.py
```

Open http://localhost:8501. Select a provider and model in the sidebar, then ask questions in the **Ask in natural language** tab.

## Example queries

### Natural language (web UI or CLI)

```bash
# CLI — using a corporate model
uv run slack-search nlq --rht-model llama-3-3-70b-instruct-fp8-dynamic \
  "who sends the most messages?"

# CLI — using a local LM Studio model
uv run slack-search nlq --llm-url http://localhost:1234/v1 \
  --llm-model qwen/qwen3.6-27b \
  "what topics did the team discuss this week?"
```

Some queries trigger a two-phase flow: the model generates SQL, the results are run, then the model synthesises a natural-language answer. This happens automatically when the question requires interpretation (trends, summaries, topic analysis).

### Raw SQL

```bash
# Top authors by message count
uv run slack-search search \
  "SELECT u.real_name, count(*) AS msgs
   FROM messages m JOIN users u ON m.user_id=u.id
   GROUP BY u.id ORDER BY msgs DESC LIMIT 10"

# Messages from the last 7 days
uv run slack-search search \
  "SELECT datetime(timestamp,'unixepoch') AS time, username, text
   FROM messages
   WHERE timestamp > unixepoch('now','-7 days')
   ORDER BY timestamp DESC LIMIT 50"

# Thread activity — most-replied messages
uv run slack-search search \
  "SELECT datetime(timestamp,'unixepoch') AS time, username, reply_count, text
   FROM messages
   WHERE thread_ts IS NULL AND reply_count > 0
   ORDER BY reply_count DESC LIMIT 20"
```

## Grep / search

Search messages by literal string (`-F`) or regular expression (`-E`). All filters are optional and can be combined.

```bash
# Literal string, all channels, all time
uv run slack-search grep -F "out of memory"

# Regex across two channels, last two weeks
uv run slack-search grep -E "error|warning" \
  --channel cost-mgmt-dev --channel forum-cost-mgmt \
  --since "2 weeks ago"

# Messages from a specific person in a date range
uv run slack-search grep -F "budget" \
  --person Martin \
  --since 2024-01-01 --until 2024-02-01

# Pattern search in a single channel
uv run slack-search grep -E "OCP|provider_uuid" --channel forum-cost-mgmt

# All messages from someone in the cost team chat
uv run slack-search grep -E ".*" --channel cost-team-chat --person David
```

Matches are highlighted in the output. The `-c/--channel` flag can be repeated for multiple channels. `-p/--person` does a partial, case-insensitive match against all name fields.

Add `-P` / `--pager` to page through results with colours preserved (equivalent to `| less -R` but without losing the highlighting):

```bash
uv run slack-search grep -E "error|warning" --channel cost-mgmt-dev -P
```

## Live search (Slack built-in search)

`live-search` queries Slack's own search API and caches any new messages into the local database, making them available for future SQL and NLQ queries.

```bash
# Search using credentials from .curl (Enterprise Slack)
uv run slack-search live-search --curl "$(cat .curl)" "out of memory"

# Slack search operators are supported
uv run slack-search live-search --curl "$(cat .curl)" \
  'error in:#cost-mgmt-dev after:2024-01-01'

# Exact phrase, custom result count, paged output
uv run slack-search live-search --curl "$(cat .curl)" '"budget cut"' -n 20 -P

# If SLACK_TOKEN / SLACK_WORKSPACE are already set in .env
uv run slack-search live-search "out of memory"
```

Supported Slack search operators: `in:#channel`, `from:@user`, `before:YYYY-MM-DD`, `after:YYYY-MM-DD`, `"exact phrase"`.

New messages found by the search are inserted into the local database via `INSERT OR IGNORE` — they never interfere with incremental channel downloads.

The web UI exposes this as the **🔍 Slack Search** tab. Load credentials in the sidebar (`.curl` file path or `SLACK_TOKEN` env var), then type a query and click **Search Slack**. Results show with match highlighting and an **Open in Slack ↗** permalink for each message.

## LLM providers

| Provider | How to configure |
|---|---|
| **RHT models.corp** | Edit `.rht_models.json` (gitignored) with model keys |
| **LM Studio** | Start LM Studio — detected automatically on `localhost:1234` |
| **OpenCode.ai** | Set `OPENCODE_API_KEY` in `.env` |
