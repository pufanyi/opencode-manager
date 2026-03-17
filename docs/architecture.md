# Architecture

## Overview

OpenCode Manager is a process supervisor + Telegram bot that bridges mobile users to multiple AI coding sessions. It supports two provider backends: **Claude Code** (CLI-based, default) and **OpenCode** (HTTP-based). An optional web dashboard provides browser-based access.

```
┌─────────────┐     ┌──────────────────────────────────────────────────────┐
│  Telegram    │     │              OpenCode Manager                        │
│  User        │◄───►│                                                      │
│  (mobile)    │     │  ┌─────────┐  ┌─────────────┐  ┌───────────┐       │
└─────────────┘     │  │   Bot   │  │   Process   │  │   Store   │       │
                     │  │         │  │   Manager   │  │  (SQLite) │       │
┌─────────────┐     │  └────┬────┘  └──────┬──────┘  └─────┬─────┘       │
│  Web        │     │       │              │               │              │
│  Dashboard  │◄───►│  ┌────┴────┐  ┌──────┴──────┐       │              │
│  (browser)  │     │  │  Web    │  │  Provider   │       │              │
└─────────────┘     │  │ Server  │  │ Abstraction │       │              │
                     │  └─────────┘  └──────┬──────┘       │              │
                     │                      │               │              │
                     │           ┌──────────┼───────────┐   │              │
                     │           │          │           │   │              │
                     │     ┌─────▼─────┐ ┌──▼────────┐ │   │              │
                     │     │claude -p  │ │ opencode  │ │   │              │
                     │     │(per prompt)│ │ serve ×N  │ │   │              │
                     │     └───────────┘ └───────────┘ │   │              │
                     │                                  │   │              │
                     └──────────────────────────────────────────────────────┘
```

## Component Responsibilities

### App (`internal/app`)

The top-level orchestrator. Wires together all components on startup:

1. Opens the SQLite store
2. Creates the process manager with a port pool
3. Initializes the Telegram bot
4. Restores previously running instances from the database
5. Pre-registers projects from config
6. Starts health checks
7. Starts the web dashboard (if enabled)
8. Runs the Telegram bot (blocking)

### Config (`internal/config`)

Loads YAML configuration with sensible defaults and environment variable overrides. Validates all required fields before the application starts.

### Store (`internal/store`)

Pure-Go SQLite database (no CGo dependency). Three tables:

**`instances`** — Persistent record of all managed instances.

```sql
id TEXT PRIMARY KEY              -- UUID
name TEXT NOT NULL UNIQUE        -- Display name
directory TEXT NOT NULL          -- Project path
port INT NOT NULL DEFAULT 0     -- Allocated port (0 for Claude Code)
password TEXT NOT NULL DEFAULT ''-- Basic Auth password (OpenCode only)
status TEXT DEFAULT 'stopped'   -- running/stopped/starting/failed
auto_start BOOLEAN DEFAULT 0
provider_type TEXT DEFAULT 'claudecode' -- "claudecode" or "opencode"
created_at, updated_at DATETIME
```

**`user_state`** — Per-Telegram-user active context.

```sql
user_id INTEGER PRIMARY KEY     -- Telegram user ID
active_instance_id TEXT         -- FK to instances
active_session_id TEXT          -- Current session ID
updated_at DATETIME
```

**`claude_sessions`** — Session tracking (used by both providers).

```sql
id TEXT PRIMARY KEY             -- Session ID
instance_id TEXT NOT NULL       -- Parent instance
title TEXT DEFAULT ''           -- Auto-titled from first prompt
created_at DATETIME
updated_at DATETIME
message_count INTEGER DEFAULT 0 -- Prompt history depth
```

Uses WAL journal mode and 5-second busy timeout for safe concurrent access.

### Provider Abstraction (`internal/provider`)

Defines a common `Provider` interface with two implementations:

**Interface methods:**
- `Start(ctx)` — Spawn process
- `Stop()` — Terminate process
- `Wait()` — Block until process exits
- `WaitReady(ctx, timeout)` — Wait for readiness
- `IsReady()` — Check readiness status
- `HealthCheck(ctx)` — Ping endpoint
- `Stderr()` — Return last error output
- `SetPort(int)` — Update port
- `CreateSession(ctx)` — New session
- `GetSession(ctx, id)` — Fetch session
- `ListSessions(ctx)` — List all sessions
- `Prompt(ctx, sessionID, content)` — Send prompt, return event channel
- `Abort(ctx, sessionID)` — Cancel running prompt

**Claude Code Provider** (`claudecode.go`):
- No persistent server process
- Spawns `claude -p` per prompt with `--output-format json`
- Uses `--resume <sessionID>` for existing sessions
- Reads JSON-streaming output, emits `StreamEvent`s
- Session tracking via SQLite `claude_sessions` table
- No port allocation needed; `WaitReady` returns immediately

**OpenCode Provider** (`opencode.go`):
- Persistent `opencode serve` child process per instance
- HTTP REST API + SSE for communication
- Basic Auth with random 32-hex password
- Config injected via `OPENCODE_CONFIG_CONTENT` env var
- Port allocated from configurable range

### Process Manager (`internal/process`)

Manages the lifecycle of all provider instances.

**Port Pool** — Thread-safe allocator over a configurable range. Ports are allocated on start, released on stop, and re-allocated on restart. Only used by OpenCode instances.

**Instance** — Wraps a `Provider` with metadata (name, directory, status, provider type).

**Crash Recovery** — A goroutine monitors each running instance. On unexpected exit:

```
Crash detected
  → Mark status = failed
  → If restarts < max (default 3):
      → Wait 2^restartCount seconds (exponential backoff)
      → Allocate new port (OpenCode only)
      → Restart provider
      → Continue monitoring
  → Else:
      → Notify all Telegram users
      → Release port
```

**Health Checks** — Periodic pings to each running instance. Failures are logged but don't trigger restarts (crash recovery handles actual process death).

### OpenCode Client (`internal/opencode`)

HTTP client for the OpenCode REST API:

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `GET` | `/` | Health check / status |
| `GET` | `/session` | List sessions |
| `POST` | `/session` | Create session |
| `GET` | `/session/:id` | Get session details |
| `GET` | `/session/:id/message` | List messages |
| `POST` | `/session/:id/prompt` | Send prompt (async) |
| `POST` | `/session/:id/abort` | Abort running prompt |

All requests use HTTP Basic Auth (empty username, instance password).

**SSE Subscriber** — Connects to `GET /event` for real-time streaming. Features:
- Auto-reconnect with 2-second retry on disconnect
- 15-second heartbeat timeout (treats silence as disconnect)
- 1 MB scanner buffer for large messages
- Event handler registry with wildcard support

### Telegram Bot (`internal/bot`)

**Auth Middleware** — Every handler checks the user ID against the allowed list. Unauthorized users are silently ignored.

**Command Routing** — Commands are matched by prefix (e.g., `/new` matches `/new`, `/new test`, `/new@bot_name`). Unrecognized messages fall through to the default handler, which forwards them as prompts. Photos are routed to the photo handler.

**Streaming Bridge** (`streaming.go`) — Translates provider events into Telegram message edits:

```
User sends text/photo
  → Bot sends placeholder "[instance] Thinking..."
  → Provider.Prompt fires, returns event channel
  → StreamContext buffers content + tool statuses
  → editLoop (5s ticker, 2s for drafts) flushes to Telegram
  → Markdown → Telegram HTML conversion with tag balancing
  → If content > 4096 chars: split into continuation messages
  → If content > 12000 chars: send as .md file
  → On completion: final edit with [Abort] [New Session] keyboard
```

**Rate Limiting:**
- Per-stream: 5-second edit coalescing interval (2s for drafts)
- Global: 25-permit semaphore (Telegram limit ≈ 30 msgs/sec, with margin)

**Message Formatting** (`format.go`) — Converts Markdown to Telegram-compatible HTML using goldmark. Handles:
- Heading/list/table/checkbox conversion to Telegram-safe equivalents
- HTML tag balancing (close unclosed tags, fix nesting violations)
- HTML-aware truncation (doesn't break tags or entities)
- UTF-8 safe truncation

### Web Dashboard (`internal/web`)

Optional Angular-based web UI, embedded in the binary via `go:embed`.

**API Endpoints:**

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `GET` | `/api/instances` | List all instances |
| `POST` | `/api/instances` | Create instance |
| `GET` | `/api/instances/:id` | Instance details |
| `POST` | `/api/instances/:id/start` | Start instance |
| `POST` | `/api/instances/:id/stop` | Stop instance |
| `DELETE/POST` | `/api/instances/:id/delete` | Delete instance |
| `GET` | `/api/instances/:id/sessions` | List sessions |
| `POST` | `/api/sessions/:id/new` | Create session |
| `POST` | `/api/prompt` | Send prompt |
| `POST` | `/api/abort` | Abort prompt |
| `GET` | `/api/ws` | SSE stream (real-time events) |

**Streaming Hub** — Broadcasts provider events to connected SSE clients. Clients can filter by session ID via `?session=<id>` query parameter.

## Data Flow

### Prompt Lifecycle

```
1. Telegram message (text or photo) arrives
2. Auth check (allowed_users)
3. Look up user_state → active_instance_id + active_session_id
4. Auto-create session if none exists (auto-title from first prompt)
5. For photos: download image to temp file, include path in prompt
6. Send placeholder Telegram message
7. Call Provider.Prompt(sessionID, content) → returns event channel
8. StreamContext reads events, buffers text + tool invocations
9. Edit timer fires → EditMessageText with Telegram HTML
10. On completion → final edit with action keyboard
11. Cleanup (temp image files, stream context)
```

### Manager Restart

```
1. Open SQLite database, run migrations
2. Query instances WHERE status='running' OR auto_start=1
3. For each: allocate new port (OpenCode), update DB, spawn provider
4. Query all instances → load stopped ones into memory
5. Start SSE listeners for running OpenCode instances
6. Pre-register projects from config (with provider type)
7. Start web dashboard (if enabled)
8. Start health check goroutine
9. Run Telegram bot (blocking)
```

## Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/go-telegram/bot` | v1.19 | Telegram Bot API client |
| `modernc.org/sqlite` | v1.46 | Pure-Go SQLite driver (no CGo) |
| `gopkg.in/yaml.v3` | v3.0 | YAML config parsing |
| `github.com/google/uuid` | v1.6 | UUID generation for instance IDs |
| `github.com/yuin/goldmark` | v1.5 | Markdown → HTML conversion |

No CGo required — the binary is fully statically linkable.
