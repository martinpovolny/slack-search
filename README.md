# slack-search

Download Slack channels to a local SQLite database. Search with grep, SQL, natural language, or through a web UI. Works as an MCP server for AI agents.

[![CI](https://github.com/martinpovolny/slack-search/actions/workflows/ci.yml/badge.svg)](https://github.com/martinpovolny/slack-search/actions/workflows/ci.yml)

![Web UI](docs/web-ui-go.png)

## Quick Start

```bash
# 1. Install
go install github.com/martinpovolny/slack-search/cmd/slack-search@latest

# 2. Get credentials — Chrome DevTools → Network → any Slack API call → Copy as cURL
pbpaste > ~/.slack-search/.curl    # macOS; on Linux: xclip -o > ~/.slack-search/.curl

# 3. Download a channel
slack-search download --channel general --since "4 weeks ago" --curl-file ~/.slack-search/.curl

# 4. Search
slack-search grep -F "outage"
```

## grep

![grep output](docs/grep-output.png)

```bash
slack-search grep -F "out of memory"                          # literal string
slack-search grep -E "error|warning" -c my-channel            # regex, specific channel
slack-search grep -F "budget" -p Alice --since "2 weeks ago"  # by person + time range
```

## SQL

```bash
slack-search search "
  SELECT u.real_name, count(*) AS msgs
  FROM messages m JOIN users u ON m.user_id = u.id
  GROUP BY u.id ORDER BY msgs DESC LIMIT 10"
```

## Web UI

```bash
slack-search serve    # includes background channel refresh
```

Open http://localhost:8088 — SQL, natural language queries, Slack live search, message permalinks.

## More Commands

| Command | What it does |
|---------|-------------|
| `download` | Download a channel (incremental) |
| `refresh` | Update all previously downloaded channels |
| `grep` | Search by text or regex with highlights |
| `search` | Raw SQL against the archive |
| `live-search` | Query Slack's search API, cache results locally |
| `nlq` | Natural language → SQL → answer (requires LLM) |
| `serve` | Web UI with background refresh |
| `mcp` | MCP server for AI agents |

Existing archives are automatically migrated the first time a newer version
opens them. This includes repair of messages affected by a past Block Kit text
duplication bug. The migration prints progress while it runs, records its
completion, and is skipped on later opens.

## Keeping Channels Up to Date

**`slack-search serve`** handles this automatically. If you only use the CLI:

```bash
slack-search refresh --curl-file ~/.slack-search/.curl --lookback 7
```

Within the lookback window, refresh re-reads channel history and re-fetches
every thread with replies, so late replies to recent parents are archived.
For important channels, set a longer per-channel window with
`slack-search channel-config --channel NAME --deep-scan-days 30`. To backfill a
single thread, run `slack-search fetch-thread <permalink>`. See
[thread replies](docs/thread-sync.md) for the cost, the remaining limit, and
how to change the one-second spacing between Slack API calls.

On macOS without `serve`, the example launchd job in
`com.user.slack-refresh.plist` runs an hourly refresh; it expects the binary
in `~/bin` (`make install`).

## AI Agent Integration

### MCP Server

```json
// ~/.cursor/mcp.json
{
  "mcpServers": {
    "slack-search": { "command": "slack-search", "args": ["mcp"] }
  }
}
```

Claude Code: `claude mcp add slack-search -- slack-search mcp`

Tools: `slack_grep`, `slack_sql`, `slack_thread`, `slack_channels`, `slack_schema`,
`slack_bookmarks`, `slack_canvases`, `slack_refresh_channel`, `slack_fetch_thread`,
`slack_set_deep_scan`

`slack_refresh_channel` fetches the latest messages for one channel, then
re-reads its history and re-fetches its threads over the scan window (seven
days by default, or the channel's `deep_scan_days` when larger), so agents can
repair a stale archive before searching it. `slack_fetch_thread` fetches one
thread by permalink, or by channel plus ts, when a linked message is missing.
Both require a valid `~/.slack-search/.curl`. See
[thread replies](docs/thread-sync.md) for why replies can be missing.

### Skill file (alternative)

Copy [`docs/slack-search-skill.md`](docs/slack-search-skill.md) to `.claude/commands/slack-search.md` — the agent calls the CLI via Bash. Includes full schema reference and query cookbook.

## Database

SQLite at `~/.slack-search/messages.db`:

```
messages  (ts, channel_id, user_id, username, text, timestamp, thread_ts, reply_count)
channels  (id, name, subscribed)
users     (id, name, real_name, display_name)
files     (id, ts, channel_id, name, mimetype, url, local_path)
```

## Python version

The `python/` directory contains the original Streamlit-based implementation, used for prototyping. The Go binary is the primary tool.

## CI

The [GitHub Actions workflow](.github/workflows/ci.yml) checks Go lint, build,
race-enabled tests and vulnerabilities; frontend lint, build and embedded
asset consistency; Python tests; and offline Markdown links.
