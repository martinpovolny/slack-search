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
| F06 | grep | done | done | Regex/literal search |
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
| F16 | NLQ chat tab | done | partial | UI present, no conversation persistence yet |
| F17 | Browse messages tab | done | done | Filters, detail view |
| F18 | SQL query tab | done | done | Raw SQL execution |
| F19 | Slack Search tab | done | partial | Placeholder — needs credential flow in UI |
| F20 | Sidebar | done | done | Channel list, stats |
| F21 | Conversation persistence | done | - | conversations.db not yet implemented |
| **Other** |
| F22 | Eval framework | done | - | Lower priority |
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

**Phase 3 — Web UI: MOSTLY COMPLETE**
1. ~~F20: Sidebar scaffold~~
2. ~~F18: SQL query tab~~
3. ~~F17: Browse messages~~
4. F16+F21: NLQ chat + conversations (partial — UI works, no persistence)
5. F19: Slack Search (placeholder — needs credential flow)

**Phase 4 — Polish:**
1. ~~F23: Launchd compatibility~~
2. F22: Eval framework
