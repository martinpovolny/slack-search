# MCP Server Design — slack-search

## Overview

Expose the slack-search archive as an MCP server so coding agents (Claude Code, Cursor, etc.) can query Slack history directly via structured tools. Single Go binary serves both the web UI (`serve`) and MCP (`mcp` subcommand over stdio).

## Transport

**stdio** (JSON-RPC) — simplest, works with all MCP clients.

```bash
slack-search mcp   # starts MCP server on stdin/stdout
```

Claude Code config (`~/.claude/settings.json`):
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

## Tools

### Core tools

| Tool | Input | Output | When to use |
|------|-------|--------|-------------|
| `slack_grep` | `query` (string), `channels` (string[]), `since`/`until` (string), `person` (string), `limit` (int), `regexp` (bool) | `[{time, channel, author, text, ts, thread_ts}]` | Find messages containing a term, error string, or regex pattern |
| `slack_sql` | `query` (SQL string) | `{columns: string[], rows: any[][]}` | Aggregations, counts, joins, rankings — anything grep can't express |
| `slack_nlq` | `question` (string), `max_rows` (int) | `{sql, answer, result, mode, error}` | Natural language question → LLM generates SQL + optional synthesis. Uses RHT models.corp. |
| `slack_channels` | (none) | `[{id, name, subscribed}]` | List available channels in the archive |
| `slack_schema` | (none) | `{schema: string}` | DDL + join examples + date function cheatsheet for writing SQL |
| `slack_stats` | (none) | `{message_count, channel_count, oldest, newest, last_refresh}` | Archive metadata — size, date range, freshness |

### Candidates for future addition

| Tool | Input | Output | When to use |
|------|-------|--------|-------------|
| `slack_thread` | `ts` (string), `channel` (string or name) | `[{time, author, text, ts}]` | Fetch a full thread by root message timestamp |
| `slack_message` | `ts` (string), `channel` (string or name) | `{time, channel, author, text, thread_ts, raw_json}` | Get a single message by ts + channel |
| `slack_users` | `query` (string, optional) | `[{id, name, real_name, display_name}]` | List or search users in the archive |

## Open questions

- **Include `slack_nlq`?** It calls an external LLM (RHT models.corp) which adds latency and cost. An agent could use `slack_schema` + `slack_sql` instead. But `slack_nlq` with synthesis is useful for summarization tasks the agent might delegate.
- **Resource exposure?** MCP supports resources (read-only data). The schema and channel list could be exposed as resources instead of tools, making them available in context without an explicit tool call.

## Implementation steps

1. Add `internal/mcp/` package — JSON-RPC stdio transport (initialize, tools/list, tools/call)
2. Register tools — each wraps an existing Go function (search.Grep, search.RunSQL, nlq.RunQuery, etc.)
3. Add `mcp` subcommand to `cmd/slack-search/main.go`
4. Tool input schemas — JSON Schema for each tool's parameters
5. Test — `echo '{"jsonrpc":"2.0",...}' | slack-search mcp`

## Libraries

- `github.com/mark3labs/mcp-go` — mature Go MCP SDK
- Or implement minimal protocol directly (3 methods: `initialize`, `tools/list`, `tools/call`)

## Advantages over current slash command

- **Structured JSON** — no text parsing
- **Multi-tool sessions** — agent can grep, then SQL, then NLQ without re-launching
- **Global access** — add to `~/.claude/settings.json`, works from any project
- **Same binary** — zero additional dependencies
