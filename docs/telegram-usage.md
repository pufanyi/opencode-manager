# Telegram Bot Usage Guide

## Getting Started

1. Open Telegram and find the bot you created via [@BotFather](https://t.me/BotFather)
2. Send `/start` to see the welcome message
3. Create your first instance:
   ```
   /new myproject /path/to/your/project
   ```
4. Send any text message — it goes straight to OpenCode as a prompt

## Concepts

### Instance

An instance is a running `opencode serve` process tied to a specific project directory. Each instance has:

- A unique name (e.g., `backend`, `frontend`)
- Its own port and auth credentials
- Independent sessions and conversation history

### Session

Each instance can have multiple sessions (conversations). A session maintains its own message history within OpenCode. You can:

- Create new sessions: `/session new`
- Switch between sessions: `/sessions` then tap one
- Prompts go to your currently active session

### Active Context

Each Telegram user has an **active instance** and **active session**. All prompts and commands operate on this context. The active context is shown in every response header:

```
[instance-name]
```

## Commands

### `/new <name> <path>`

Create and start a new OpenCode instance.

```
/new backend /home/user/projects/backend-api
```

- Allocates a port from the configured range
- Starts `opencode serve` in the given directory
- Automatically switches you to the new instance
- Creates an SSE listener for real-time streaming

### `/list`

Shows all instances with status indicators:

- 🟢 Running
- 🟡 Starting
- 🔴 Stopped / Failed

Tap an instance in the inline keyboard to switch to it.

### `/switch <name>`

Switch your active instance by name.

```
/switch frontend
```

This clears your active session — you'll need to select or create one in the new instance.

### `/stop [name]`

Stop an instance. If no name is given, stops your active instance.

```
/stop backend
```

### `/start_inst <name>`

Restart a stopped instance.

```
/start_inst backend
```

A new port is allocated (the old one was released on stop).

### `/status`

Shows your current context:

```
Active Instance: backend
Status: running
Directory: /home/user/projects/backend-api
Port: 14096
Session: abc123
```

### `/session new`

Create a new session in the active instance.

```
/session new
```

### `/session`

Show info about your current session.

### `/sessions`

List all sessions in the active instance. Tap one to switch to it.

### `/abort`

Abort the currently running prompt. Useful when a response is taking too long or going in the wrong direction.

### `/help`

Show all available commands.

## Sending Prompts

Any text message that isn't a command is sent as a prompt to your active instance and session.

**What happens:**

1. A placeholder message appears: *"Thinking..."*
2. The prompt is sent to OpenCode via HTTP API
3. SSE events stream back as OpenCode processes the request
4. The placeholder is progressively edited with the response
5. Tool invocations appear with status icons:
   - ⏳ Running
   - ✅ Completed
   - ❌ Error
6. When done, action buttons appear: **[Abort]** and **[New Session]**

### Long Responses

- Messages are automatically split at Telegram's 4096-character limit
- Very long responses (>12,000 characters) are sent as a `.md` file attachment

### Rate Limiting

Updates are batched every 1.5 seconds to stay within Telegram's API limits. You won't see every character, but the response builds up progressively.

## Inline Keyboard Actions

Many commands produce inline keyboards. Available actions:

| Button | Action |
|--------|--------|
| 🟢/🔴 Instance Name | Switch to that instance |
| **Stop** | Stop the instance |
| **Start** | Start a stopped instance |
| **Delete** | Remove instance permanently |
| **Switch** | Switch active context |
| Session ID/Title | Switch to that session |
| **Abort** | Abort the running prompt |
| **New Session** | Create a fresh session |

## Tips

- **Quick start**: `/new proj /path` → immediately type your prompt
- **Multiple projects**: Use `/list` and tap to switch between instances rapidly
- **Fresh context**: Tap "New Session" after finishing a topic to start clean
- **Check status**: `/status` shows what instance and session you're talking to
- **Emergency stop**: `/abort` if a prompt is running too long
