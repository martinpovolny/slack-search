# slack-search (Go)

slack-search archives Slack conversations in SQLite and provides CLI search,
an HTTP API, a React web UI, and an MCP server. The Go implementation is the
primary application. `python/` contains the original implementation used for
prototyping. This file is the canonical repository guidance; `CLAUDE.md` points
here so the instructions stay in one place.

## Architecture

- Go HTTP backend for business logic, LLM integration, and data access.
- Vite and React frontend built to static assets and embedded with `//go:embed`.
- Single Go binary with no runtime dependencies.
- See `go-embedded-ui-pattern.md` for embedded UI design requirements.

## Code map

- `cmd/slack-search/main.go`: CLI entry point and command dispatch.
- `internal/api/`: HTTP handlers and routes.
- `internal/db/`: SQLite schema, migrations, queries, and conversation storage.
- `internal/slack/`: Slack client, cURL credential parser, and live search.
- `internal/download/`: channel downloads, refresh, threads, bookmarks, and canvases.
- `internal/search/`, `internal/nlq/`, `internal/format/`: search, natural language queries, and message formatting.
- `internal/mcpserver/`: MCP server over stdio.
- `internal/web/embed.go`: embeds static assets from `internal/web/dist/`.
- `ui/ui/`: React, TypeScript, Vite, and Tailwind source and npm manifest.
- `python/`: original Python implementation and shared archive schema.
- `prompts/`: LLM prompts; `docs/`: feature catalog, design notes, and usage docs.
- `completions/`: shell completions; `Makefile`: build and development commands.

## Build and run

- Build the Go binary: `make build` (outputs `bin/slack-search`).
- Build frontend source: from `ui/ui/`, run `npm ci` and `npm run build`.
- Run the backend: `go run ./cmd/slack-search serve` (default port 8088).
- Run the frontend in development: from `ui/ui/`, run `npm run dev` (port 5173).
- Run the compiled binary: `./bin/slack-search`.

The Makefile currently sets `UI_DIR := ./ui`, but the npm project is in
`ui/ui/`. Its `all`, `ui-build`, and `dev` targets therefore need attention
before use. Vite builds into `ui/ui/dist/`, while Go embeds `internal/web/dist/`;
building the frontend alone does not update the embedded assets. Inspect this
wiring when changing UI delivery. Edit frontend source rather than generated
bundles. Vite proxies `/api` and `/healthz` to the backend on port 8088.

## Data and configuration

Runtime data lives under `~/.slack-search/`, independent of the working
directory:

| File | Purpose |
|---|---|
| `messages.db` | Slack message archive, shared with the Python version |
| `conversations.db` | Natural language query conversation history |
| `.curl` | Slack credentials copied from Chrome as cURL |
| `.rht_models.json` | RHT LLM provider configuration |

The `serve` command detects `~/.slack-search/.curl`; `--curl-file path` can
override it. Keep machine-specific paths out of code. Treat `.curl`, `.env`,
`.rht_models.json`, `.slack_app`, databases, and downloaded Slack content as
private. Never commit credentials or real conversations; use synthetic test
data. Do not refresh channels or alter the user's archive to validate code.

## SQLite schema

The Go schema is defined in `internal/db/database.go` and must stay in sync
with `python/slack_search/database.py`. Before changing it, inspect dependent
queries and schema prompts. Preserve compatibility with existing archives.

## LLM provider

- RHT models.corp is the supported provider, configured in
  `~/.slack-search/.rht_models.json` or the project root `.rht_models.json`.
- Use its OpenAI-compatible API and configured URL template.
- Do not add LiteMaaS, LM Studio, OpenCode, or other providers without an
  explicit change to the project requirements.

## Background refresh

The `serve` command runs a background refresh goroutine by default. It refreshes
every 30 minutes with a 7-day thread lookback. `--no-refresh` disables it,
`--refresh-interval N` sets the interval in minutes, and `--lookback N` sets the
thread catchup window in days. The goroutine recovers from panics and logs
errors; expired credentials are reported and retried at the next tick.

For CLI-only use, `refresh --lookback N` runs a one-shot refresh. Use a larger
lookback, such as 30 days, for a one-time gap fill.

## Design constraints

- Put API routes under `/api/`, serve static assets at `/`, and preserve the
  SPA fallback for non-API, non-asset routes.
- Read configuration from environment variables or `~/.slack-search/`; avoid
  hardcoded machine-specific paths.
- Slack API calls use POST with the token in the form body, never an
  Authorization header. Enterprise credentials need an `xoxc-` token and all
  cookies from the copied cURL request.
- Preserve the Slack client's minimum one-second spacing between API calls
  and its rate-limit handling.
- Keep MCP stdio output protocol-safe; send diagnostics to stderr.
- Update `docs/go-feature-catalog.md` when a catalogued feature changes, and
  update usage documentation when commands or behavior change.

## Checks

- Build Go: `go build ./cmd/slack-search`.
- Run Go tests: `go test ./...` or `make test`.
- Format changed Go files with `gofmt`; run `go vet ./...` for static checks.
- CI also runs race-enabled tests with coverage and a 120-second timeout,
  golangci-lint, govulncheck, and offline link checks for `README.md` and
  `docs/**/*.md`.
- Frontend checks, from `ui/ui/`: `npm run build` and `npm run lint`.

Use temporary databases and fake clients in automated tests. Do not require
personal Slack credentials, a live LLM, or the user's archive for checks.

## Feature parity

Track Go feature parity in `docs/go-feature-catalog.md` and update its Go
column when a feature lands.
