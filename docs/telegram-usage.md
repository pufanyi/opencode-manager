# Telegram Bot Usage Guide

## Getting Started

1. Open Telegram and find the bot you created via [@BotFather](https://t.me/BotFather)
2. Send `/start` to see the welcome message
3. Create your first instance:
   ```
   /new myproject /path/to/your/project
   ```
4. Send any text message — it goes straight to Claude Code as a prompt

## Concepts

### Instance

An instance is a managed AI coding process tied to a specific project directory. There are two types:

- **Claude Code** (default) — Uses the `claude` CLI. No persistent server; spawns per prompt.
- **OpenCode** — Uses `opencode serve`. Runs as a persistent HTTP server with its own port and auth credentials.

Each instance has:

- A unique name (e.g., `backend`, `frontend`)
- A provider type (CC = Claude Code, OC = OpenCode)
- Independent sessions and conversation history

### Session

Each instance can have multiple sessions (conversations). A session maintains its own message history. You can:

- Create new sessions: `/session new`
- Switch between sessions: `/sessions` then tap one
- Prompts go to your currently active session

Sessions are auto-created when you send your first prompt. The first message is used as the session title.

### Active Context

Each Telegram user has an **active instance** and **active session**. All prompts and commands operate on this context. Use `/status` to see your current context.

## Commands

### `/new <name> <path>`

Create and start a new **Claude Code** instance.

```
/new backend /home/user/projects/backend-api
```

- Starts a Claude Code instance in the given directory
- Automatically switches you to the new instance
- A session is auto-created when you send your first prompt

### `/newopencode <name> <path>`

Create and start a new **OpenCode** instance.

```
/newopencode frontend /home/user/projects/frontend
```

- Allocates a port from the configured range
- Starts `opencode serve` in the given directory
- Creates an SSE listener for real-time streaming

### `/list`

Shows all instances with status indicators and provider type:

- 🟢 [CC] backend — running
- 🟡 [OC] frontend — starting
- 🔴 [CC] legacy — stopped

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

For OpenCode instances, a new port is allocated (the old one was released on stop).

### `/status`

Shows your current context:

```
Active Instance: backend
Provider: Claude Code
Status: running
Directory: /home/user/projects/backend-api
Session: fix authentication bug (5 msgs)
```

### `/session new`

Create a new session in the active instance.

```
/session new
```

### `/session`

Show info about your current session, including title, message count, and last activity time.

### `/sessions`

List all sessions in the active instance. Shows:

- Session title (auto-titled from first prompt)
- Message count
- Last activity time
- Active session indicator (▶)

Tap a session to switch to it. Limited to 20 sessions in the list.

### `/abort`

Abort the currently running prompt. Useful when a response is taking too long or going in the wrong direction.

### `/help`

Show all available commands.

## Sending Prompts

Any text message that isn't a command is sent as a prompt to your active instance and session.

**What happens:**

1. A placeholder message appears: `[instance-name] Thinking...`
2. The prompt is sent to the provider (Claude Code or OpenCode)
3. Streaming events arrive as the AI processes the request
4. The placeholder is progressively edited with the response (converted to Telegram HTML)
5. Tool invocations appear with status icons:
   - ⏳ Running
   - ✅ Completed
   - ❌ Error
6. When done, action buttons appear: **[Abort]** and **[New Session]**

### Sending Photos

You can send photos to Claude Code instances. The image is downloaded and passed as a file path in the prompt. Include a caption to provide context, or the default prompt "Please analyze this image" is used.

Photos are stored temporarily and cleaned up after the prompt completes.

### Long Responses

- Messages are automatically split at Telegram's 4096-character limit
- Very long responses (>12,000 characters) are sent as a `.md` file attachment

### Rate Limiting

Updates are batched every 5 seconds (2 seconds for drafts in private chats) to stay within Telegram's API limits. You won't see every character, but the response builds up progressively.

## Inline Keyboard Actions

Many commands produce inline keyboards. Available actions:

| Button | Action |
|--------|--------|
| 🟢/🔴 Instance Name | Switch to that instance |
| **Stop** | Stop the instance |
| **Start** | Start a stopped instance |
| **Delete** | Remove instance permanently |
| **Switch** | Switch active context |
| Session Title | Switch to that session |
| **Abort** | Abort the running prompt |
| **New Session** | Create a fresh session |

## Tips

- **Quick start**: `/new proj /path` → immediately type your prompt
- **Multiple projects**: Use `/list` and tap to switch between instances rapidly
- **Fresh context**: Tap "New Session" after finishing a topic to start clean
- **Check status**: `/status` shows what instance, provider, and session you're talking to
- **Emergency stop**: `/abort` if a prompt is running too long
- **Visual analysis**: Send a photo with a caption to have Claude Code analyze it
- **Provider choice**: Use `/new` for Claude Code (default, recommended), `/newopencode` for OpenCode
