# OpenCode Manager

A single-binary tool that manages multiple [OpenCode](https://github.com/sst/opencode) instances on one server, controlled entirely via a Telegram bot. Run AI coding sessions across different projects from your phone.

## Features

- **Multi-instance** — Run many OpenCode instances side by side, each in its own project directory
- **Telegram interface** — Create, start, stop, switch, and prompt instances from any Telegram client
- **Real-time streaming** — See AI responses stream in as progressive Telegram message edits
- **Crash recovery** — Auto-restarts crashed instances with exponential backoff and notifies you on permanent failure
- **Persistent state** — SQLite database tracks instances and per-user context across restarts
- **Single binary** — Built-in setup wizard, no external scripts needed

## Quick Start

```bash
# Build
make build

# Interactive setup (generates opencode-manager.yaml)
./bin/opencode-manager setup

# Run
./bin/opencode-manager -config opencode-manager.yaml
```

### Prerequisites

- Go 1.22+
- [OpenCode](https://github.com/sst/opencode) installed and available in `$PATH`
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- Your Telegram user ID (send `/start` to [@userinfobot](https://t.me/userinfobot))

## Setup Wizard

The built-in wizard walks you through configuration step by step:

```
$ opencode-manager setup

  ┌──────────────────────────────────────┐
  │     OpenCode Manager Setup Wizard    │
  └──────────────────────────────────────┘

Step 1/6: Telegram Bot Token
Step 2/6: Allowed Telegram User IDs
Step 3/6: OpenCode Binary Path
Step 4/6: Port Range
Step 5/6: Database Path
Step 6/6: Pre-register Projects (optional)
```

It generates `opencode-manager.yaml` with proper permissions (`0600`).

You can also specify the output path:

```bash
opencode-manager setup -output ~/.config/opencode-manager/opencode-manager.yaml
```

## Configuration

The config file can be placed in any of these locations (checked in order):

1. Path passed via `-config` flag
2. `OPENCODE_MANAGER_CONFIG` environment variable
3. `./opencode-manager.yaml`
4. `./configs/opencode-manager.yaml`
5. `~/.config/opencode-manager/opencode-manager.yaml`

See [`configs/opencode-manager.example.yaml`](configs/opencode-manager.example.yaml) for a full example.

### Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `TELEGRAM_TOKEN` | `telegram.token` |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` (comma-separated) |
| `OPENCODE_BINARY` | `process.opencode_binary` |
| `STORAGE_DATABASE` | `storage.database` |

## Telegram Commands

### Instance Management

| Command | Description |
|---------|-------------|
| `/new <name> <path>` | Create & start a new OpenCode instance |
| `/list` | List all instances with status indicators |
| `/switch <name>` | Switch your active instance |
| `/start_inst <name>` | Start a stopped instance |
| `/stop [name]` | Stop an instance (active if no name given) |

### Session & Prompting

| Command | Description |
|---------|-------------|
| `/session new` | Create a new session in the active instance |
| `/session` | Show current session info |
| `/sessions` | List all sessions (tap to switch) |
| `/abort` | Abort the running prompt |
| _any text_ | Send as a prompt to the active instance |

### General

| Command | Description |
|---------|-------------|
| `/start` | Welcome message and quick-start guide |
| `/help` | Show all commands |
| `/status` | Active instance, session, and connection details |

## How It Works

```
You (Telegram) ──→ Bot ──→ OpenCode Manager ──→ opencode serve (HTTP API)
       ↑                                              │
       └──────── SSE streaming ◄──────────────────────┘
```

1. The manager spawns `opencode serve` child processes, each on its own port with its own auth token
2. When you send a text message, it's forwarded as a prompt to your active instance via HTTP
3. The instance streams its response back via SSE (Server-Sent Events)
4. The streaming bridge progressively edits your Telegram message with rate-limited updates

### Process Isolation

Each OpenCode instance runs with:

- **Dedicated port** allocated from a configurable range (default 14096–14196)
- **Unique password** for HTTP Basic Auth
- **Config injection** via `OPENCODE_CONFIG_CONTENT` env var (no files modified in the project directory)

### Crash Recovery

When an instance crashes unexpectedly:

1. The manager detects the exit via `cmd.Wait()`
2. Restarts with exponential backoff (1s, 2s, 4s, ...)
3. After 3 failures (configurable), marks as permanently failed
4. Sends a Telegram notification to all authorized users

### Streaming to Telegram

The streaming bridge handles Telegram's constraints:

- **Rate limiting** — Edits are coalesced into 1.5-second intervals with a global 25-request semaphore
- **Message splitting** — Auto-splits at 4096 characters (Telegram's limit)
- **File fallback** — Sends as a `.md` file if the response exceeds ~12,000 characters
- **Tool indicators** — Shows tool invocations with status icons (⏳ running, ✅ done, ❌ error)

## Architecture

```
cmd/opencode-manager/
└── main.go                  Entry point, subcommand routing, signal handling

internal/
├── setup/setup.go           Interactive setup wizard
├── config/config.go         YAML config + env overrides + validation
├── store/
│   ├── store.go             SQLite connection (WAL mode, pure Go)
│   ├── migrations.go        Schema: instances, user_state tables
│   ├── instance.go          Instance CRUD
│   └── userstate.go         Per-user active instance/session tracking
├── process/
│   ├── portpool.go          Thread-safe port allocation
│   ├── instance.go          Process lifecycle (spawn/stop/wait)
│   └── manager.go           Orchestrator: create, health check, crash recovery
├── opencode/
│   ├── types.go             API types (Session, Message, SSEEvent, etc.)
│   ├── client.go            HTTP REST client (sessions, prompts, abort)
│   └── sse.go               SSE subscriber with auto-reconnect
├── bot/
│   ├── bot.go               Telegram bot setup, auth middleware
│   ├── handlers.go          Command handlers + prompt forwarding
│   ├── callbacks.go         Inline keyboard callback handlers
│   ├── keyboard.go          Inline keyboard builders
│   └── streaming.go         SSE-to-Telegram bridge with rate limiting
└── app/app.go               Application orchestrator wiring everything together
```

## Building

```bash
# Build binary
make build

# Build and run
make run

# Clean build artifacts
make clean
```

## License

MIT
