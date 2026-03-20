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

Pure-Go SQLite database (no CGo dependency). Four tables:

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
worktree_path TEXT DEFAULT ''   -- Git worktree directory (empty = main dir)
branch TEXT DEFAULT ''          -- Git branch name (empty = no worktree)
```

**`message_sessions`** — Maps Telegram messages to sessions (enables reply-to-continue).

```sql
chat_id INTEGER NOT NULL        -- Telegram chat ID
message_id INTEGER NOT NULL     -- Telegram message ID
session_id TEXT NOT NULL        -- Session that produced this message
PRIMARY KEY (chat_id, message_id)
```

Uses WAL journal mode and 5-second busy timeout for safe concurrent access.

### Provider Abstraction (`internal/provider`)

Defines a common `Provider` interface with two implementations:

**Interface methods:**
- `Type()` — Return provider backend type
- `Start(ctx)` — Spawn process
- `Stop()` — Terminate process
- `Wait()` — Block until process exits
- `WaitReady(ctx, timeout)` — Wait for readiness
- `IsReady()` — Check readiness status
- `HealthCheck(ctx)` — Ping endpoint
- `Stderr()` — Return last error output
- `SetPort(int)` — Update port
- `CreateSession(ctx, opts)` — New session (opts controls worktree creation)
- `GetSession(ctx, id)` — Fetch session
- `ListSessions(ctx)` — List all sessions
- `SupportsWorktree()` — Whether this provider can create git worktrees
- `Prompt(ctx, sessionID, content)` — Send prompt, return event channel
- `Abort(ctx, sessionID)` — Cancel running prompt

**StreamEvent types:** `text`, `tool_use`, `done`, `error`, `merge_failed`

**Claude Code Provider** (`claudecode.go`):
- No persistent server process
- Spawns `claude -p` per prompt with `--output-format stream-json`
- Uses `--resume <sessionID>` for existing sessions
- Reads JSON-streaming output, emits `StreamEvent`s
- Session tracking via SQLite `claude_sessions` table
- No port allocation needed; `WaitReady` returns immediately
- **Git worktree support**: creates isolated worktrees per session (`SupportsWorktree()` returns true for git repos)
- **Auto-merge**: after prompt completion, merges session branch back to main and rebases other active worktrees
- **DeleteSession**: removes worktree directory and branch

**OpenCode Provider** (`opencode.go`):
- Persistent `opencode serve` child process per instance
- HTTP REST API + SSE for communication
- Basic Auth with random 32-hex password
- Config injected via `OPENCODE_CONFIG_CONTENT` env var
- Port allocated from configurable range
- No worktree support (`SupportsWorktree()` returns false)

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

**Worktree Choice** — When a Claude Code instance is in a git repo, new sessions and prompts show an inline keyboard asking "🌿 New Worktree" or "📂 Main Directory". The choice is stored as a pending prompt until the user picks.

**Reply-to-Continue** — When a user replies to a bot response, the handler looks up the session that produced that message (via `message_sessions` table) and continues it, bypassing the worktree choice flow.

**Active Tasks Board** (`streaming.go`) — A consolidated live status message at the bottom of each chat:

```
User sends text/photo
  → For git repos: show worktree choice keyboard, wait for selection
  → Create session (with or without worktree)
  → Provider.Prompt fires, returns event channel
  → StreamContext buffers content + tool statuses
  → Active Tasks board refreshes on a timer (default 2s):
      - Shows all running tasks as blockquote cards
      - Displays tool invocations with ⏳/✅/❌ icons and details
      - "Stop #N" buttons to cancel individual tasks
      - Repositions to bottom when new messages appear
  → On completion:
      - Final response sent as reply to user's original message
      - Markdown → Telegram HTML conversion with tag balancing
      - If content > 4096 chars: split into continuation messages
      - If content > 12000 chars: send as .md file
      - Auto-merge worktree branch back to main (if applicable)
      - Board removes completed task (disappears when all tasks done)
```

**Rate Limiting:**
- Global: 25-permit semaphore (Telegram limit ≈ 30 msgs/sec, with margin)

**Merge Notifications** — After a prompt completes in a worktree session:
- Success: "✅ merged N commit(s) from branch into main"
- Failure: "⚠️ Auto-merge failed" with a "🔧 Fix with Claude" button that creates a new session to resolve the conflict

**Message Formatting** (`format.go`) — Converts Markdown to Telegram-compatible HTML using goldmark. Handles:
- Heading/list/table/checkbox conversion to Telegram-safe equivalents
- HTML tag balancing (close unclosed tags, fix nesting violations)
- HTML-aware truncation (doesn't break tags or entities)
- UTF-8 safe truncation

### Git Operations (`internal/gitops`)

Provides git worktree merge-back logic used by `streaming.go` after prompt completion:

- Detects main/master branch names
- Handles both linked worktrees (merge from main worktree) and regular repos (fast-forward ref update)
- Aborts on merge conflict and reports the error
- Used as a secondary merge path alongside the provider's own `mergeAndSync`

### Firebase Communication Layer (`internal/firebase`)

The primary mechanism for the web frontend to communicate with the Go backend. Both sides are "clients" that make outbound HTTPS connections to Firebase — **no direct connection, no public IP, no open ports**.

**Key design:** Go signs in as a **regular Firebase user** (email/password or refresh token) via the REST API. It does NOT use the Admin SDK or a service account.

```
Web Frontend (Angular)          Firebase RTDB              Go Backend
       │                            │                           │
       │  onValue(/instances) ◄─────┤◄── Set every 2s ─────────┤  sync.go
       │  onValue(/presence)  ◄─────┤◄── Update every 30s ─────┤  presence.go
       │  onValue(/streams)   ◄─────┤◄── Update every 300ms ───┤  streamer.go
       │                            │                           │
       │── push(/commands) ────────►│                           │
       │   {action, status:pending} │──► SSE Listen ───────────►┤  commands.go
       │                            │◄── Update status:done ────┤
       │  onValue(status change) ◄──┤                           │
```

**Components:**

| File | Purpose | Mechanism |
|------|---------|-----------|
| `auth.go` | Firebase Auth via REST API (email/password or refresh token) | `identitytoolkit.googleapis.com`, `securetoken.googleapis.com` |
| `rtdb.go` | RTDB REST client: Get, Set, Update, Delete, Listen (SSE) | HTTP `?auth=<idToken>` |
| `sync.go` | Syncs local instance list to `/instances` | Periodic PUT every 2s |
| `presence.go` | Heartbeats to `/presence/{instanceId}` | Periodic PATCH every 30s |
| `streamer.go` | Buffers provider StreamEvents to `/streams/{sessionId}` | Periodic PATCH every 300ms |
| `commands.go` | Watches `/commands` via SSE, executes & updates status | SSE + PATCH |
| `config_sync.go` | Pull/push app config from `/config` | GET/PUT + SSE wait |
| `client.go` | Ties together Auth, RTDB, Streamer, Presence, Commands | Initialization |

**RTDB Data Model (actual implementation):**

```
/instances/{instanceId}          ← Written by Go (sync.go), read by web
    id, name, directory, status, provider_type

/presence/{instanceId}           ← Written by Go (presence.go), read by web
    online: boolean, last_seen: number

/streams/{sessionId}             ← Written by Go (streamer.go), read by web
    content, status, tool_calls[], error?, updated_at

/commands/{instanceId}/{cmdId}   ← Written by web, read+updated by Go (commands.go)
    action, payload, status, user_id, created_at, updated_at, result?, error?

/config                          ← Read/written by both (config_sync.go)
    telegram_token, binary paths, etc.

/link_codes/{code}               ← Optional: for Telegram account linking
    uid, expires

/users/{uid}/telegram_id         ← Optional: for Telegram account linking
```

**Note:** The design doc (`web-frontend-design.md`) originally planned Firestore for persistent data (instances, sessions, history). The actual implementation uses **RTDB only** — Firestore is not used.

### Embedded Web Dashboard (`internal/web`)

Fallback local web UI embedded in the binary via `go:embed`. Used when Firebase is not enabled.

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

### Web Prompt Lifecycle (via Firebase)

```
1.  User types prompt in web UI
2.  Web writes to RTDB: /commands/{instanceId}/{cmdId}
    { action: "prompt", payload: { session_id, content }, status: "pending" }
3.  Go CommandListener (SSE) detects new command
4.  Go updates status → "ack"
5.  Go calls Provider.Prompt(sessionID, content) → returns event channel
6.  Streamer.WrapEvents wraps the channel, buffering events to RTDB:
    /streams/{sessionId} updated every 300ms with content + tool_calls
7.  Web frontend onValue(/streams/{sessionId}) fires on each update → re-renders
8.  On completion: Go updates stream status → "complete", command status → "done"
9.  Web sees status change, updates UI
```

### Telegram Prompt Lifecycle

```
1.  Telegram message (text or photo) arrives
2.  Auth check (allowed_users)
3.  If reply to a bot message → look up session from message_sessions table
4.  Otherwise: look up user_state → active_instance_id
5.  For photos: download image to temp file, include path in prompt
6.  If new session needed and provider supports worktree:
    → Show "🌿 New Worktree" / "📂 Main Directory" choice keyboard
    → Store pending prompt until user selects
7.  Create session (with or without worktree) and auto-title from first prompt
8.  Call Provider.Prompt(sessionID, content) → returns event channel
9.  StreamContext reads events, buffers text + tool invocations
10. Active Tasks board refreshes periodically, showing tool progress
11. On completion → send final response as reply to original message
12. Auto-merge worktree branch back to main (if applicable)
    → On merge failure: send notification with "Fix with Claude" button
13. Board removes task; disappears when all tasks complete
14. Cleanup (temp image files, stream context)
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
