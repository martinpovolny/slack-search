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

## LLM providers

| Provider | How to configure |
|---|---|
| **RHT models.corp** | Edit `.rht_models.json` (gitignored) with model keys |
| **LM Studio** | Start LM Studio — detected automatically on `localhost:1234` |
| **OpenCode.ai** | Set `OPENCODE_API_KEY` in `.env` |
