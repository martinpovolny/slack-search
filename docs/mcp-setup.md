# slack-search MCP Server

An MCP (Model Context Protocol) server that gives coding agents direct access to your local Slack archive. Works with Claude Code, Cursor, and any MCP-compatible client.

## Setup

### 1. Build the binary

```bash
cd go && make all
```

Or if already built, ensure `slack-search` is in your PATH:

```bash
ln -sf ~/Projects/slack-search/go/bin/slack-search ~/bin/slack-search
```

### 2. Configure your MCP client

**Claude Code** — add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "slack-search": {
      "command": "slack-search",
      "args": ["mcp"]
    }
  }
}
```

**Cursor** — add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "slack-search": {
      "command": "slack-search",
      "args": ["mcp"]
    }
  }
}
```

### 3. Verify

In Claude Code, ask: *"Use the slack_channels tool to list my Slack channels."*

The agent should call `slack_channels` and return the list of subscribed channels from your archive.

## Tools

### `slack_grep`

Search messages by keyword or regex. Returns results with `<@UXXXXXXX>` mentions resolved to real names.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | yes | Search text (literal unless `regexp=true`) |
| `channels` | string | no | Comma-separated channel names |
| `since` | string | no | After this date (e.g. `"3 weeks ago"`, `"2024-01-01"`) |
| `until` | string | no | Before this date |
| `person` | string | no | Filter by sender name (partial match) |
| `limit` | number | no | Max results (default 50) |
| `regexp` | boolean | no | Treat query as a regular expression |

**Example prompt:** *"Search for OOM errors in cost-mgmt-prod-alerts from the last 2 weeks."*

### `slack_sql`

Execute a raw SQL query against the archive. Only SELECT queries are allowed.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | yes | SQL query (SELECT or WITH only) |

**Example prompt:** *"Who sent the most messages in cost-mgmt-dev this month?"*

The agent will call `slack_schema` first to learn the tables, then write and execute SQL via `slack_sql`.

### `slack_thread`

Fetch all messages in a thread by the parent message timestamp. Use after finding an interesting message via `slack_grep` to read the full discussion.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `thread_ts` | string | yes | Timestamp of the parent message |
| `channel` | string | yes | Channel name or ID |

**Example prompt:** *"Show me the full thread for message 1782489113.049369 in cost-mgmt-dev."*

### `slack_channels`

List subscribed channels — the ones with complete message history.

No parameters. Returns `[{id, name}]`.

### `slack_schema`

Database schema reference: table DDL, useful joins, SQLite date function cheatsheet, and the archive's date range.

No parameters. Call this before writing SQL so the agent knows the table and column names.

## How agents use it

A typical agent workflow:

1. **`slack_channels`** — see what channels are available
2. **`slack_grep`** — find messages about the topic
3. **`slack_thread`** — read the full discussion for an interesting result
4. **`slack_sql`** — aggregate data (counts, rankings, time series)

The agent calls `slack_schema` before writing SQL to get the correct table definitions and SQLite syntax.

## Architecture

```
┌──────────────────┐         stdio          ┌─────────────────┐
│  Claude Code /   │ ◄──── JSON-RPC ─────► │  slack-search    │
│  Cursor / Agent  │                        │  mcp             │
└──────────────────┘                        │                  │
                                            │  ~/.slack-search │
                                            │  /messages.db    │
                                            └─────────────────┘
```

The MCP server opens the shared SQLite database read-only and serves tool calls over stdin/stdout. It's the same binary that powers the web UI (`slack-search serve`) and the CLI.

## Data

The archive at `~/.slack-search/messages.db` is kept fresh by the background refresh goroutine in `slack-search serve` (every 30 minutes by default), or by the launchd cron job, or by manual `slack-search refresh` runs.

All tools are read-only — the MCP server never modifies the database.
