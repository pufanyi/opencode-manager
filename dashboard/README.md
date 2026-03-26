# Local Dashboard

Angular 21 single-page application for managing OpenCode Manager instances locally. Communicates directly with the Go server via REST API and SSE -- no Firebase dependency.

## Architecture

Unlike the remote web frontend (`web/`), the local dashboard talks directly to the Go HTTP server:

- **REST API** (`/api/*`) -- instance CRUD, sessions, prompts
- **SSE** (`/api/ws`) -- real-time streaming of prompt responses

No authentication is required -- the dashboard runs on the same machine as the Go server.

## Development

```bash
# From the project root:
make dev          # Starts Go server + Angular dev server with HMR

# Or manually:
cd dashboard
pnpm install
pnpm start        # ng serve on http://localhost:4200
```

In dev mode (`-dev` flag), the Go server reverse-proxies non-API requests to the Angular dev server for hot module replacement.

## Building

```bash
make dashboard    # Builds and copies output to internal/web/dist for embedding
```

The build output is embedded into the Go binary via `go:embed`.

## Project Structure

```
src/app/
├── services/
│   └── api.service.ts              HTTP client + SSE connection to Go server
├── components/
│   ├── instance-list/              Instance management (create/start/stop/delete)
│   ├── prompt-panel/               Session selector, prompt input, SSE streaming display
│   └── settings/                   Telegram bot status, app settings
├── app.component.ts                Root component
└── app.config.ts                   App configuration
```

## Tech Stack

| Layer | Choice |
|-------|--------|
| Framework | Angular 21 (standalone components) |
| TypeScript | 5.9 |
| Bundler | Angular CLI (esbuild) |
| Linter | Biome |
| Communication | `fetch()` + `EventSource` (no external HTTP library) |
