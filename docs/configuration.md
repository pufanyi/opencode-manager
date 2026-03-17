# Configuration Reference

## Config File Locations

The manager searches for config files in this order:

1. `-config` CLI flag
2. `OPENCODE_MANAGER_CONFIG` environment variable
3. `./opencode-manager.yaml`
4. `./configs/opencode-manager.yaml`
5. `~/.config/opencode-manager/opencode-manager.yaml`

Run `opencode-manager setup` to generate a config interactively.

## Full Example

```yaml
telegram:
  token: "123456789:ABCdefGHIjklMNOpqrsTUVwxyz"
  allowed_users: [123456789, 987654321]

process:
  opencode_binary: "opencode"
  claudecode_binary: "claude"
  port_range:
    start: 14096
    end: 14196
  health_check_interval: 30s
  max_restart_attempts: 3

projects:
  - name: "backend"
    directory: "/home/user/projects/backend"
    provider: "claudecode"
    auto_start: true
  - name: "frontend"
    directory: "/home/user/projects/frontend"
    provider: "opencode"
    auto_start: false

storage:
  database: "./data/opencode-manager.db"

web:
  enabled: true
  addr: ":8080"
```

## Section Reference

### `telegram`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `token` | string | Yes | Bot token from [@BotFather](https://t.me/BotFather) |
| `allowed_users` | int64[] | Yes | Telegram user IDs authorized to use the bot |

Only users in `allowed_users` can interact with the bot. All other messages are silently ignored.

### `process`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `opencode_binary` | string | `"opencode"` | Path to the OpenCode binary (or name for `$PATH` lookup) |
| `claudecode_binary` | string | `"claude"` | Path to the Claude Code binary (or name for `$PATH` lookup) |
| `port_range.start` | int | `14096` | First port in the allocation range |
| `port_range.end` | int | `14196` | Last port (exclusive), giving 100 slots by default |
| `health_check_interval` | duration | `30s` | How often to ping running instances |
| `max_restart_attempts` | int | `3` | Consecutive crash restarts before giving up |

Port range must be within 1024–65535 and `start < end`. Ports are only used by OpenCode instances; Claude Code instances don't require port allocation.

### `projects`

Optional list of pre-registered projects. Each entry:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | — | Unique display name for the instance |
| `directory` | string | — | Absolute path to the project directory |
| `provider` | string | `"claudecode"` | Provider type: `"claudecode"` or `"opencode"` |
| `auto_start` | bool | `false` | Start this instance automatically on manager startup |

Projects listed here are created on first launch. On subsequent launches, existing instances with matching names are loaded from the database — only `auto_start` behavior is re-applied.

### `storage`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `database` | string | `"./data/opencode-manager.db"` | Path to the SQLite database file |

The parent directory is created automatically if it doesn't exist. The database uses WAL journal mode for safe concurrent access.

### `web`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the web dashboard |
| `addr` | string | `":8080"` | Listen address for the HTTP server |

When enabled, an Angular-based web dashboard is served with REST API endpoints and SSE streaming.

## Environment Variable Overrides

Environment variables take precedence over the config file:

| Variable | Config Field | Format |
|----------|-------------|--------|
| `TELEGRAM_TOKEN` | `telegram.token` | String |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` | Comma-separated integers |
| `OPENCODE_BINARY` | `process.opencode_binary` | String |
| `CLAUDECODE_BINARY` | `process.claudecode_binary` | String |
| `STORAGE_DATABASE` | `storage.database` | String |

Example:

```bash
TELEGRAM_TOKEN="123:ABC" CLAUDECODE_BINARY="/usr/local/bin/claude" opencode-manager
```

## Security Notes

- The config file contains your Telegram bot token. The setup wizard writes it with `0600` permissions.
- Each OpenCode instance gets a unique random password for HTTP Basic Auth. Passwords are stored in the SQLite database.
- Only users in `allowed_users` can control the bot.
- All OpenCode instances bind to `127.0.0.1` (localhost only) — they are not exposed to the network.
- Claude Code instances don't require network ports; they communicate via stdin/stdout.
