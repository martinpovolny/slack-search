# Go + Vite Embedded UI — Design Document & Agent Guidelines

> **Purpose:** This document defines the architecture, non-functional requirements, and implementation guidelines for building web applications as a **single Go binary with an embedded frontend**. It is intended to be used as a system prompt / instruction set for a coding agent.

---

## 1. Pattern Overview

The pattern combines:

- A **Go HTTP backend** that owns all business logic, LLM integration, and data access
- A **Vite + React frontend** built to static assets at compile time
- Go's `//go:embed` directive to bake the static assets into the binary
- A **single deployable artifact**: one binary, zero runtime dependencies

At development time, the frontend dev server proxies API calls to the Go backend. At production time, the Go binary serves the frontend itself.

```
┌─────────────────────────────────────────┐
│              Single Binary              │
│                                         │
│  ┌─────────────┐    ┌────────────────┐  │
│  │  Go Backend │    │  Embedded      │  │
│  │  (HTTP API) │    │  Static Assets │  │
│  │             │    │  (dist/)       │  │
│  └──────┬──────┘    └───────┬────────┘  │
│         │                   │           │
│         └────────┬──────────┘           │
│                  │                      │
│           net/http mux                  │
└──────────────────┼──────────────────────┘
                   │
              Browser
```

---

## 2. Non-Functional Requirements

### 2.1 Deployment

- **NFR-D1**: The application MUST ship as a single self-contained binary with no external file dependencies at runtime.
- **NFR-D2**: The binary MUST run on Linux (amd64 and arm64) and macOS (arm64) without modification.
- **NFR-D3**: No Node.js, npm, or any JavaScript runtime is required on the deployment target.
- **NFR-D4**: The binary MUST be buildable via a single `make build` command that compiles both frontend and backend.
- **NFR-D5**: Cross-compilation MUST be supported: `GOOS=linux GOARCH=arm64 go build` must produce a working binary after `make ui-build`.

### 2.2 Developer Experience

- **NFR-DX1**: Hot-module replacement (HMR) MUST work during frontend development — saving a `.tsx` file must update the browser in under 500 ms.
- **NFR-DX2**: The Go backend MUST be restartable independently of the frontend dev server (use `air` or `go run ./cmd/...` separately).
- **NFR-DX3**: A single `make dev` target SHOULD start both the Go backend and the Vite dev server concurrently.
- **NFR-DX4**: Frontend API calls during development MUST be proxied to the Go backend via Vite's `server.proxy` config — no CORS workarounds, no hardcoded ports in frontend code.
- **NFR-DX5**: The production build MUST be testable locally via `make run` before embedding (`vite preview` is acceptable for frontend-only; the full binary for end-to-end).

### 2.3 Performance

- **NFR-P1**: Binary startup time MUST be under 500 ms on target hardware (this is trivially achievable with Go; do not add init-time work that blocks the HTTP server).
- **NFR-P2**: Static assets served from the embedded FS MUST be served with appropriate `Cache-Control` headers (at minimum `max-age=3600` for hashed assets).
- **NFR-P3**: The Vite production build MUST enable code splitting and tree shaking (default Rollup behaviour — do not disable it).
- **NFR-P4**: Assets MUST be gzip-compressed at serve time if the client sends `Accept-Encoding: gzip`. Use `compress/gzip` in Go middleware or a wrapper around `http.FileServer`.

### 2.4 Security

- **NFR-S1**: The Go HTTP server MUST NOT expose directory listings. Use `http.FS` with `embed.FS`, not `http.Dir`.
- **NFR-S2**: API routes and static asset routes MUST be clearly separated on the mux (e.g., `/api/` prefix for all backend routes).
- **NFR-S3**: The binary MUST NOT embed `.env` files, secrets, or development-only configuration. Secrets come from environment variables or flags at runtime.
- **NFR-S4**: If the UI includes a Content-Security-Policy header, it MUST be set by the Go server, not hardcoded in HTML.

### 2.5 Observability

- **NFR-O1**: The Go server MUST log all requests (method, path, status, duration) to stdout in a structured format (JSON preferred).
- **NFR-O2**: A `/healthz` endpoint MUST return `200 OK` with `{"status":"ok"}` and MUST NOT be embedded behind the frontend catch-all.
- **NFR-O3**: Build metadata (git commit, build time) SHOULD be injected at build time via `-ldflags` and exposed at `/healthz` or `/version`.

### 2.6 Maintainability

- **NFR-M1**: Frontend and backend code MUST live in the same repository.
- **NFR-M2**: The `dist/` directory (Vite output) MUST be listed in `.gitignore`. It is a build artifact, not source.
- **NFR-M3**: The Go module path and the frontend package name MUST reflect the application name consistently.
- **NFR-M4**: The Makefile MUST be the single source of truth for build, run, and dev commands — no undocumented manual steps.

---

## 3. Repository Layout

```
myapp/
├── cmd/
│   └── myapp/
│       └── main.go          # entrypoint; starts HTTP server
├── internal/
│   ├── api/                 # HTTP handlers, route registration
│   ├── service/             # business logic
│   └── web/
│       └── embed.go         # //go:embed directive lives here
├── ui/                      # Vite + React project root
│   ├── src/
│   │   ├── components/      # React components
│   │   ├── App.tsx
│   │   └── main.tsx
│   ├── dist/                # ← build output; embedded by Go; gitignored
│   ├── index.html
│   ├── vite.config.ts
│   ├── tsconfig.json
│   └── package.json
├── .gitignore
├── go.mod
├── go.sum
└── makefile
```

Key rule: the `//go:embed` path is relative to the Go file that declares it. Keep `embed.go` in a dedicated `internal/web/` package to keep the directive path clean.

---

## 4. Implementation Specification

### 4.1 Go Embed

```go
// internal/web/embed.go
package web

import "embed"

//go:embed all:dist
var StaticFiles embed.FS
```

`all:` prefix includes dotfiles if any exist in `dist/`. Without it, files starting with `.` or `_` are silently skipped.

### 4.2 HTTP Mux Setup

```go
// internal/api/routes.go
package api

import (
    "io/fs"
    "net/http"

    "github.com/yourorg/myapp/internal/web"
)

func NewMux() http.Handler {
    mux := http.NewServeMux()

    // Backend routes — registered first, more specific wins
    mux.HandleFunc("/healthz", handleHealthz)
    mux.HandleFunc("/api/", handleAPI)

    // Frontend catch-all — must be last
    uiFS, _ := fs.Sub(web.StaticFiles, "dist")
    mux.Handle("/", http.FileServer(http.FS(uiFS)))

    return mux
}
```

The `fs.Sub` call strips the `dist/` prefix so that `dist/index.html` is served at `/index.html` (i.e., `/`).

### 4.3 SPA Routing (if needed)

If the React app uses client-side routing (React Router, TanStack Router), the Go server must serve `index.html` for all non-API, non-asset 404s:

```go
type spaHandler struct {
    fs http.Handler
    fsys fs.FS
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Try serving the file; if 404, serve index.html
    _, err := h.fsys.Open(r.URL.Path)
    if err != nil {
        r.URL.Path = "/"
    }
    h.fs.ServeHTTP(w, r)
}
```

### 4.4 Vite Configuration

```ts
// ui/vite.config.ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8088',
        changeOrigin: true,
      },
      '/healthz': {
        target: 'http://localhost:8088',
      },
    },
  },
})
```

### 4.5 Makefile

```makefile
BINARY     := myapp
GO_PKG     := ./cmd/myapp
UI_DIR     := ./ui
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -ldflags "-X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

.PHONY: all build ui-build run dev clean

## Build everything: frontend then Go binary
all: ui-build build

## Build the Go binary (requires ui-build to have been run)
build:
	go build $(LDFLAGS) -o bin/$(BINARY) $(GO_PKG)

## Build the Vite frontend
ui-build:
	cd $(UI_DIR) && npm ci && npm run build

## Run the compiled binary
run: all
	./bin/$(BINARY)

## Development mode: start Go backend + Vite dev server concurrently
dev:
	@echo "Starting Go backend on :8088 and Vite dev server on :5173"
	go run $(GO_PKG) & \
	cd $(UI_DIR) && npm run dev

## Cross-compile for Linux ARM64 (e.g. Oracle Cloud Ampere)
build-linux-arm64: ui-build
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64 $(GO_PKG)

## Remove build artifacts
clean:
	rm -rf bin/ $(UI_DIR)/dist
```

### 4.6 .gitignore additions

```
# Frontend build output — generated artifact, not source
ui/dist/

# Node dependencies
ui/node_modules/

# Go binary output
bin/
```

---

## 5. UI Layout Convention (Streamlit-like)

For AI/chat/tool applications, the reference layout is:

```
┌──────────────┬────────────────────────────────┐
│              │                                │
│  Left Panel  │        Main Panel              │
│  (sidebar)   │        (content / chat)        │
│              │                                │
│  - Settings  │  - Primary interaction area    │
│  - Nav       │  - Output display              │
│  - Controls  │  - Input at bottom             │
│              │                                │
└──────────────┴────────────────────────────────┘
```

Implement this using **shadcn/ui** components:

```bash
# From ui/ directory
npx shadcn@latest init
npx shadcn@latest add sidebar
npx shadcn@latest add scroll-area
npx shadcn@latest add input
npx shadcn@latest add button
```

The `SidebarProvider` + `Sidebar` + `SidebarContent` components from shadcn give you a collapsible, responsive sidebar that matches this layout out of the box. Wire the right panel content via React Router or conditional rendering in `App.tsx`.

---

## 6. Streaming Responses (SSE)

For LLM streaming or real-time updates, use Server-Sent Events over a plain HTTP endpoint — no WebSocket dependency needed:

**Go side:**
```go
func handleStream(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming not supported", http.StatusInternalServerError)
        return
    }

    for token := range tokenStream {
        fmt.Fprintf(w, "data: %s\n\n", token)
        flusher.Flush()
    }
}
```

**React side:**
```ts
const es = new EventSource('/api/chat/stream')
es.onmessage = (e) => setMessages(prev => [...prev, e.data])
es.onerror   = () => es.close()
```

---

## 7. Build & Verify Checklist

The coding agent MUST verify each of the following before considering a task complete:

- [ ] `make ui-build` runs without errors and produces `ui/dist/index.html`
- [ ] `make build` produces `bin/myapp` and the binary includes embedded assets (`strings bin/myapp | grep -c "<!DOCTYPE html"` returns > 0)
- [ ] `./bin/myapp` starts and `curl localhost:8088/healthz` returns `200`
- [ ] `curl localhost:8088/` returns the React app HTML (not a directory listing)
- [ ] `curl localhost:8088/api/nonexistent` returns a JSON error, not HTML
- [ ] `curl localhost:8088/nonexistent-route` returns `index.html` (if SPA routing is enabled)
- [ ] `GOOS=linux GOARCH=arm64 go build ...` compiles without errors (after ui-build)
- [ ] `ui/dist/` is present in `.gitignore`
- [ ] No secrets or `.env` content appears in the binary (`strings bin/myapp | grep -i secret` returns nothing)

---

## 8. What the Pattern Does NOT Cover

These are explicitly out of scope and require separate decisions:

- Authentication / session management
- Database integration
- TLS termination (handle at the reverse proxy / load balancer layer)
- Multi-instance / clustering (this is a single-process binary)
- CI/CD pipeline specifics (the Makefile targets are the interface; plug them into whatever CI)

---

## 9. Reference Implementations

- `ardanlabs/ai-training` — Example 09 Step 4 and Example 13 Step 4: Go + Kronk LLM backend with embedded Vite+React chat UI
- `ardanlabs/kronk` — model server with OpenWebUI-compatible API, pairs well as the LLM backend in this pattern
