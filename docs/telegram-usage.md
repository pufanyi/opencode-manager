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
- Delete sessions: tap 🗑 in the session list
- Prompts go to your currently active session

Sessions are auto-created when you send your first prompt. The first message is used as the session title.

**Worktree sessions** — For Claude Code instances in a git repo, each session can optionally run in its own git worktree. When creating a session or sending a new prompt, you'll be asked to choose "🌿 New Worktree" (isolated branch) or "📂 Main Directory". Worktree sessions are automatically merged back to the main branch after each prompt completes.

### Active Context

Each Telegram user has an **active instance** and **active session**. All prompts and commands operate on this context. Use `/status` to see your current context.

You can also **reply to any bot response** to continue that specific session, even if you've since switched to a different instance or session.

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

For Claude Code instances in git repos, this will prompt you to choose between a new worktree or the main directory.

### `/session`

Show info about your current session, including title, message count, last activity time, and branch name (if worktree).

### `/sessions`

List all sessions in the active instance. Shows:

- Session title (auto-titled from first prompt)
- Message count
- Last activity time
- Active session indicator (▶)

Tap a session to switch to it. Tap 🗑 to delete a session (also removes its worktree and branch if applicable). A "+ New Session" button at the bottom creates a fresh session. Limited to 20 sessions in the list.

### `/abort`

Abort the currently running prompt. Useful when a response is taking too long or going in the wrong direction.

### `/help`

Show all available commands.

## Sending Prompts

Any text message that isn't a command is sent as a prompt to your active instance and session.

**What happens:**

1. If this is your first prompt (no active session) and the instance is a git repo:
   - A keyboard appears asking "🌿 New Worktree" or "📂 Main Directory"
   - Your choice determines whether the session runs in an isolated branch
2. The prompt is sent to the provider (Claude Code or OpenCode)
3. The **Active Tasks board** appears showing real-time progress:
   - Tool invocations with status icons (⏳ running, ✅ done, ❌ error) and details
   - Elapsed time for each task
   - "Stop #N" buttons to cancel tasks
4. When done, the final response is sent as a **reply** to your original message
5. For worktree sessions: the branch is auto-merged back to main

### Replying to Continue

You can **reply to any bot response** to continue that specific session with a new prompt. This works even if you've switched to a different instance or session — the reply automatically targets the correct session.

### Sending Photos

You can send photos to Claude Code instances. The image is downloaded and passed as a file path in the prompt. Include a caption to provide context, or the default prompt "Please analyze this image" is used.

Photos are stored temporarily and cleaned up after the prompt completes.

### Long Responses

- Messages are automatically split at Telegram's 4096-character limit
- Very long responses (>12,000 characters) are sent as a `.md` file attachment

### Active Tasks Board

While any prompts are running, a live status message appears at the bottom of the chat. It shows all active tasks as blockquote cards with:

- Task number, instance name, and elapsed time
- Session location (🌿 worktree or 📂 main dir) and title
- Tool invocations with status icons and details (e.g., file paths, command descriptions)
- "Stop #N" inline buttons to cancel individual tasks

The board refreshes at a configurable interval (default 2s, set via `telegram.board_interval`). It repositions to the bottom when new messages appear and disappears automatically when all tasks finish.

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
| 🗑 | Delete session (and its worktree/branch if any) |
| **+ New Session** | Create a fresh session |
| **🌿 New Worktree** | Create session in an isolated git worktree |
| **📂 Main Directory** | Create session in the project's main directory |
| **Stop #N** | Stop a specific running task (on the Active Tasks board) |
| **🔧 Fix with Claude** | Create a session to resolve a merge conflict |

## Tips

- **Quick start**: `/new proj /path` → immediately type your prompt
- **Multiple projects**: Use `/list` and tap to switch between instances rapidly
- **Fresh context**: Use `/sessions` and tap "+ New Session" to start clean
- **Reply to continue**: Reply to any bot response to keep talking to that session
- **Parallel work**: Use worktrees to run multiple sessions on the same repo without conflicts
- **Check status**: `/status` shows what instance, provider, and session you're talking to
- **Emergency stop**: Use "Stop #N" on the Active Tasks board, or `/abort` for the active session
- **Visual analysis**: Send a photo with a caption to have Claude Code analyze it
- **Provider choice**: Use `/new` for Claude Code (default, recommended), `/newopencode` for OpenCode
- **Merge conflicts**: If auto-merge fails, tap "🔧 Fix with Claude" to let Claude resolve it
