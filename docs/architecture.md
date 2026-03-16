# Architecture

## Overview

OpenCode Manager is a process supervisor + Telegram bot that bridges mobile users to multiple OpenCode AI coding sessions. It spawns `opencode serve` child processes and communicates with them entirely over HTTP.

```
┌─────────────┐     ┌─────────────────────────────────────────────────┐
│  Telegram    │     │              OpenCode Manager                   │
│  User        │◄───►│                                                 │
│  (mobile)    │     │  ┌─────────┐  ┌─────────────┐  ┌───────────┐  │
└─────────────┘     │  │   Bot   │  │   Process   │  │   Store   │  │
                     │  │         │  │   Manager   │  │  (SQLite) │  │
                     │  └────┬────┘  └──────┬──────┘  └─────┬─────┘  │
                     │       │              │               │         │
                     │       │    HTTP + SSE│               │         │
                     │       │      ┌───────▼───────┐       │         │
                     │       │      │ opencode serve│×N     │         │
                     │       └─────►│ (child procs) │       │         │
                     │              └───────────────┘       │         │
                     └─────────────────────────────────────────────────┘
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
7. Runs the Telegram bot (blocking)

### Config (`internal/config`)

Loads YAML configuration with sensible defaults and environment variable overrides. Validates all required fields before the application starts.

### Store (`internal/store`)

Pure-Go SQLite database (no CGo dependency). Two tables:

**`instances`** — Persistent record of all managed OpenCode instances.

```sql
id TEXT PRIMARY KEY          -- UUID
name TEXT NOT NULL UNIQUE    -- Display name
directory TEXT NOT NULL      -- Project path
port INT NOT NULL            -- Allocated port
password TEXT NOT NULL       -- Basic Auth password
status TEXT DEFAULT 'stopped'
auto_start BOOLEAN DEFAULT 0
created_at, updated_at DATETIME
```

**`user_state`** — Per-Telegram-user active context.

```sql
user_id INTEGER PRIMARY KEY  -- Telegram user ID
active_instance_id TEXT      -- FK to instances
active_session_id TEXT       -- OpenCode session ID
updated_at DATETIME
```

Uses WAL journal mode and 5-second busy timeout for safe concurrent access.

### Process Manager (`internal/process`)

Manages the lifecycle of `opencode serve` child processes.

**Port Pool** — Thread-safe allocator over a configurable range. Ports are allocated on start, released on stop, and re-allocated on restart.

**Instance** — Wraps an `exec.Cmd`. Spawns `opencode serve` with:
- `OPENCODE_CONFIG_CONTENT` — JSON config injected via env var (avoids touching project files)
- `OPENCODE_SERVER_PASSWORD` — Random 32-hex-char password for HTTP Basic Auth
- Working directory set to the project path

**Crash Recovery** — A goroutine calls `cmd.Wait()` on each instance. On unexpected exit:

```
Crash detected
  → Mark status = failed
  → If restarts < max (default 3):
      → Wait 2^restartCount seconds (exponential backoff)
      → Allocate new port
      → Restart process
      → Continue monitoring
  → Else:
      → Notify all Telegram users
      → Release port
```

**Health Checks** — Periodic `GET /` with Basic Auth to each running instance. Failures are logged but don't trigger restarts (crash recovery handles actual process death).

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

**Command Routing** — Commands are matched by prefix (e.g., `/new` matches `/new`, `/new test`, `/new@bot_name`). Unrecognized messages fall through to the default handler, which forwards them as prompts.

**Streaming Bridge** — The most complex component. Translates SSE events into Telegram message edits:

```
User sends text
  → Bot sends placeholder "_Thinking..._" → records message ID
  → PromptAsync fires to OpenCode
  → SSE events arrive
  → StreamContext buffers content, marks dirty
  → editLoop (1.5s ticker) flushes to Telegram
  → If content > 4096 chars: split into continuation messages
  → If content > 12000 chars: send as .md file
  → On completion: final edit with [Abort] [New Session] keyboard
```

**Rate Limiting:**
- Per-stream: 1.5-second edit coalescing interval
- Global: 25-permit semaphore (Telegram limit ≈ 30 msgs/sec, with margin)

## Data Flow

### Prompt Lifecycle

```
1. Telegram message arrives
2. Auth check (allowed_users)
3. Look up user_state → active_instance_id + active_session_id
4. Auto-create session if none exists
5. Send placeholder Telegram message
6. Create StreamContext (registered by session ID)
7. POST /session/:id/prompt to OpenCode instance
8. SSE subscriber receives message.created / message.updated events
9. Wildcard handler routes event to StreamContext by session ID
10. StreamContext extracts text + tool invocations, buffers content
11. Edit timer fires → EditMessageText with current buffer
12. On message.finished → final edit with action keyboard
```

### Manager Restart

```
1. Open SQLite database
2. Query instances WHERE status='running' OR auto_start=1
3. For each: allocate new port, update DB, spawn process
4. Query all instances → load stopped ones into memory
5. Start SSE listeners for running instances
6. Resume normal operation
```

## Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/go-telegram/bot` | v1.19 | Telegram Bot API client |
| `modernc.org/sqlite` | v1.46 | Pure-Go SQLite driver (no CGo) |
| `gopkg.in/yaml.v3` | v3.0 | YAML config parsing |
| `github.com/google/uuid` | v1.6 | UUID generation for instance IDs |

No CGo required — the binary is fully statically linkable.
