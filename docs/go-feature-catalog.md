# Go Port — Feature Catalog & Parity Table

Feature tracking for the Go reimplementation of slack-search.

**Legend:** `done` = implemented and tested, `partial` = partially implemented, `-` = not started, `n/a` = not applicable

## Feature Parity

| Code | Feature | Python | Go | Notes |
|------|---------|--------|-----|-------|
| **CLI** |
| F01 | download | done | done | Download channel messages with curl/token auth |
| F02 | refresh | done | done | Incremental update of subscribed channels |
| F03 | search (raw SQL) | done | done | Execute SQL, table output |
| F04 | schema | done | done | Show DB schema |
| F05 | nlq | done | done | NL→SQL with synthesis (not live-tested yet) |
| F06 | grep | done | done | Regex/literal search, color highlights, mention resolution |
| F07 | live-search | done | done | Slack search API + local caching (CLI only) |
| **Core** |
| F08 | Slack API client | done | done | POST form-body auth, rate limiting, retries |
| F09 | Curl parser | done | done | Extract credentials from Chrome curl |
| F10 | Auth error detection | done | done | Detect expired credentials, exit code 2 |
| F11 | Database: schema + migrations | done | done | Shared schema, WAL mode, FK |
| F12 | Database: CRUD operations | done | done | Upsert, insert, state tracking |
| F13 | NLQ: LLM pipeline | done | done | Schema→prompt→SQL→execute→synthesise |
| F14 | NLQ: RHT models.corp provider | done | done | .rht_models.json config |
| F15 | Text formatting | done | done | Mention resolution, highlighting |
| **Web UI** |
| F16 | NLQ chat tab | done | done | Chat with conversation list, persistence |
| F17 | Browse messages tab | done | done | Filters, detail view |
| F18 | SQL query tab | done | done | Raw SQL execution |
| F19 | Slack Search tab | done | done | Credential input in UI, live results |
| F20 | Sidebar | done | done | Channel list, stats |
| F21 | Conversation persistence | done | done | conversations.db with create/list/load/delete |
| **Other** |
| F22 | Eval framework | done | done | JSON test cases, SQL checks, summary output |
| F23 | Scheduled refresh (launchd) | done | done | Same plist works with Go binary |

## Implementation Order

**Phase 2 — Core (CLI-testable after each step): COMPLETE**
1. ~~F11+F12: Database layer~~
2. ~~F08+F09+F10: Slack API client~~
3. ~~F01+F02: Download + refresh~~
4. ~~F03+F04: SQL search + schema~~
5. ~~F06: Grep~~
6. ~~F13+F14: NLQ pipeline~~
7. ~~F07: Live search~~
8. ~~F15: Text formatting~~

**Phase 3 — Web UI: COMPLETE**
1. ~~F20: Sidebar scaffold~~
2. ~~F18: SQL query tab~~
3. ~~F17: Browse messages~~
4. ~~F16+F21: NLQ chat + conversations~~
5. ~~F19: Slack Search~~

**Phase 4 — Polish: COMPLETE**
1. ~~F23: Launchd compatibility~~
2. ~~F22: Eval framework~~

## Known Gaps (Go vs Python)

| # | Area | Gap | Priority |
|---|------|-----|----------|
| ~~G01~~ | ~~CLI~~ | ~~`--since` human date parsing~~ | ~~done~~ |
| ~~G02~~ | ~~CLI~~ | ~~`-P` pager flag~~ | ~~done~~ |
| ~~G03~~ | ~~CLI~~ | ~~SQLite REGEXP function~~ | ~~done~~ |
| G04 | CLI | No `live-search` subcommand (only via web UI API) | low |
| G05 | CLI | No `--check-missing` gap filling in download | low |
| ~~G06~~ | ~~Web UI~~ | ~~Browse: Slack deep-links~~ | ~~done~~ |
| G07 | Web UI | Browse: no keyword highlighting in message detail view | medium |
| G08 | Web UI | NLQ: no streaming response display | medium |
| G09 | Web UI | NLQ: no channel filter in prompt augmentation | medium |
| ~~G10~~ | ~~Web UI~~ | ~~NLQ: max-rows selector~~ | ~~done~~ |
| G11 | Web UI | Sidebar: no schema reference expander | low |

## Backlog

| Code | Feature | Notes |
|------|---------|-------|
| F24 | User name filter in sidebar/browse | Filter by login, full name; prototype in Python first, then port to Go |
| F25 | MCP server | Expose slack-search as an MCP server — lets Claude Code, Cursor, and other agents query the Slack archive directly via tools (search, grep, nlq). Go binary would serve both the web UI and MCP over stdio/SSE. |
| F26 | Jira ticket linking | Config: base Jira URL + list of project prefixes (e.g. COST, FLPATH). Patterns like COST-454 auto-rendered as clickable links in messages (Browse, Ask, Search tabs). Config in `~/.slack-search/config.json`. |
