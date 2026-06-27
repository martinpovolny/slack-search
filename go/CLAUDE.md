# slack-search (Go)

Go reimplementation of the slack-search tool. Single self-contained binary with embedded Vite+React frontend.

## Architecture

- **Go HTTP backend** — all business logic, LLM integration, data access
- **Vite + React + shadcn/ui frontend** — built to static assets, embedded via `//go:embed`
- **Single binary** — zero runtime dependencies
- See `go-embedded-ui-pattern.md` for full NFRs and patterns

## Building & Running

```bash
# Build everything (frontend + Go binary)
make all

# Build Go binary only (requires ui-build to have run)
make build

# Development mode (Go backend :8088 + Vite HMR :5173)
make dev

# Run tests
make test

# Run the binary
./bin/slack-search
```

## Data Directory

Everything lives under `~/.slack-search/` — the binary has zero dependency on the working directory.

| File | Purpose |
|---|---|
| `~/.slack-search/messages.db` | Message archive (shared with Python version) |
| `~/.slack-search/conversations.db` | NLQ conversation history |
| `~/.slack-search/.curl` | Slack credentials (Chrome "Copy as cURL") |
| `~/.slack-search/.rht_models.json` | RHT LLM provider config |

The `serve` command auto-detects `.curl` from `~/.slack-search/.curl`. Override with `--curl-file path`.

Schema is defined in `internal/db/database.go` — must stay in sync with `../slack_search/database.py`. Never modify the schema without updating both.

## LLM Provider

- **RHT models.corp only** — reads config from `~/.slack-search/.rht_models.json` (or project root `.rht_models.json`)
- OpenAI-compatible API via the URL template in the config file
- No other providers (no LiteMaaS, LM Studio, OpenCode)

## Key Design Rules

- All config from env vars or `~/.slack-search/` — no hardcoded paths
- API routes under `/api/` prefix, static assets at `/`
- SPA routing: serve `index.html` for non-API, non-asset 404s
- Slack API client uses POST with token in form body (not Authorization header)
- Enterprise Slack: `xoxc-` token + all cookies from `--curl`
- Rate limiting: 1 req/s minimum between Slack API calls

## Testing After Changes

1. `go build ./cmd/slack-search` — must compile
2. `go test ./...` — must pass
3. Binary opens existing `~/.slack-search/messages.db` without error
4. `curl localhost:8088/healthz` returns `{"status":"ok",...}`

## File Layout

```
go/
├── cmd/slack-search/main.go     — entrypoint
├── internal/
│   ├── api/                     — HTTP handlers + routes
│   ├── db/database.go           — SQLite schema, CRUD, migrations
│   ├── slack/                   — Slack API client + curl parser
│   ├── search/                  — SQL search, grep
│   ├── nlq/                     — NL→SQL pipeline + RHT provider
│   ├── download/                — Download + refresh logic
│   ├── format/                  — Mention resolution, highlighting
│   └── web/embed.go             — //go:embed directive
├── ui/                          — Vite + React + shadcn
├── prompts/                     — System prompts for LLM
├── Makefile                     — Single source of truth for build commands
└── go-embedded-ui-pattern.md    — Architecture NFRs
```

## Feature Parity

Track progress in `../docs/go-feature-catalog.md`. Update the Go column after each feature lands.
