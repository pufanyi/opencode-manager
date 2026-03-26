# Frontend Architecture

## Overview

OpenCode Manager has **two Angular 21 frontends**, each serving a different access pattern:

| Frontend | Directory | Communication | Auth | Use Case |
|----------|-----------|--------------|------|----------|
| **Local dashboard** | `dashboard/` | Direct HTTP API + SSE to Go server | None (same machine) | Local access, zero latency |
| **Remote web frontend** | `web/` | Firebase RTDB + Firestore relay | Firebase Auth (Google/email) | Remote access, no public IP needed |

Both frontends are Angular 21 standalone-component apps using TypeScript 5.9 and zone.js 0.16.

## Architecture

```
                        +------------------------------+
                        |    Go Server (your machine)   |
                        |                               |
+-------------+         |  +---------+  +-----------+  |
|   Local     |<--HTTP->|  |  Web    |  |  Process  |  |
|  Dashboard  |<--SSE---|  | Server  |  |  Manager  |  |
|  (embedded) |         |  | :8080   |  |           |  |
|             |         |  | /api/*  |  +-----+-----+  |
| Components: |         |  | /api/ws |        |        |
| - Instances |         |  +---------+  +-----v-----+  |
| - Prompt    |         |               | Providers |  |
| - Settings  |         |               | (claude,  |  |
+-------------+         |               |  opencode)|  |
                        |               +-----------+  |
                        +----------+-------------------+
                                   |
                        +----------v-------------------+
                        |        Firebase               |
                        |  +-------------------------+  |
+-------------+         |  | Auth (Google / email)   |  |
|  Remote     |<--RTDB--|  +-------------------------+  |
|  Web App    |         |  | RTDB (streams, commands,|  |
|  (Angular)  |--write->|  |  presence)              |  |
|             |         |  +-------------------------+  |
| Components: |         |  | Firestore (instances,   |  |
| - Login     |<--read--|  |  sessions, messages)    |  |
| - Dashboard |         |  +-------------------------+  |
| - Instances |         +------------------------------+
| - Prompt    |
+-------------+
```

---

## Local Dashboard (`dashboard/`)

### How It Works

The local dashboard is embedded in the Go binary at build time and served at the configured HTTP address (default `:8080`). It communicates directly with the Go server via REST API and SSE -- no Firebase round-trip.

### Service: `ApiService`

`dashboard/src/app/services/api.service.ts`

All communication goes through `ApiService`:

- **Instances**: `getInstances()`, `createInstance()`, `startInstance()`, `stopInstance()`, `deleteInstance()` -- HTTP to `/api/instances/*`
- **Sessions**: `getSessions(instanceId)`, `createSession(instanceId)` -- HTTP to `/api/instances/{id}/sessions`, `/api/sessions/{id}/new`
- **Prompt**: `sendPrompt(instanceId, sessionId, content)`, `abort(instanceId, sessionId)` -- HTTP POST to `/api/prompt`, `/api/abort`
- **Streaming**: `connectStream(sessionId, onEvent)` -- `EventSource` on `/api/ws?session={sessionId}`
- **Settings**: `getSettings()` -- HTTP GET to `/api/settings`

### Interfaces

```typescript
interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  port?: number;
}

interface Session {
  ID: string;
  Title: string;
}

interface StreamEvent {
  session_id: string;
  event: {
    type: string;        // "text", "tool_use", "done", "error"
    text?: string;
    toolName?: string;
    toolState?: string;  // "running", "completed", "error"
    toolDetail?: string;
    done?: boolean;
    error?: string;
  };
}

interface Settings {
  web: boolean;
  telegram_configured: boolean;
  telegram_connected: boolean;
}
```

### Components

| Component | File | Purpose |
|-----------|------|---------|
| `InstanceListComponent` | `dashboard/src/app/components/instance-list/` | Lists all instances with status, create/start/stop/delete controls |
| `PromptPanelComponent` | `dashboard/src/app/components/prompt-panel/` | Session selector, prompt input, real-time streaming display via SSE |
| `SettingsComponent` | `dashboard/src/app/components/settings/` | Shows Telegram bot status and application settings |

### Data Flow

```
User types prompt
  --> ApiService.sendPrompt()           POST /api/prompt
  --> ApiService.connectStream()        EventSource /api/ws?session=...
  --> Go handler calls Provider.Prompt()
  --> StreamHub broadcasts events via SSE
  --> PromptPanel renders text + tool calls in real time
```

### REST API Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/instances` | List all instances |
| POST | `/api/instances` | Create instance `{name, directory, provider}` |
| POST | `/api/instances/{id}/start` | Start instance |
| POST | `/api/instances/{id}/stop` | Stop instance |
| POST | `/api/instances/{id}/delete` | Delete instance |
| GET | `/api/instances/{id}/sessions` | List sessions |
| POST | `/api/sessions/{id}/new` | Create session |
| POST | `/api/prompt` | Send prompt `{instance_id, session_id, content}` |
| POST | `/api/abort` | Abort running prompt `{instance_id, session_id}` |
| GET | `/api/ws?session={id}` | SSE stream filtered by session |
| GET | `/api/settings` | Get application settings |

### Development

```bash
make dev    # Go with Angular dev server (HMR) for dashboard
```

In dev mode, the Go server reverse-proxies non-API requests to the Angular dev server (`ng serve` on port 4200), enabling hot module replacement.

---

## Remote Web Frontend (`web/`)

### How It Works

The web frontend communicates with the Go server entirely through **Firebase** (Realtime Database + Firestore). Both sides are Firebase clients making outbound HTTPS connections -- there is no direct connection between them.

**Key property:** No public IP, no port forwarding, no tunnels. The Go server only makes outbound HTTPS requests to Firebase.

All data paths are **user-scoped** under `users/{uid}/`. Each authenticated user can only access their own data.

### Firebase Services Used

| Service | Purpose | Go Side | Web Side |
|---------|---------|---------|----------|
| Auth | User login/registration | REST API (`identitytoolkit.googleapis.com`) | `firebase/auth` JS SDK |
| Realtime Database | Runtime state, streaming, commands, presence | REST API (`{db}.firebaseio.com`) | `firebase/database` JS SDK |
| Cloud Firestore | Persistent data (instances, sessions, messages, config) | REST API (`firestore.googleapis.com`) | `firebase/firestore` JS SDK |

### Authentication

Go signs in as a regular Firebase user using a stored `refresh_token`:

```
Go Server                                Firebase Auth REST API
    |                                          |
    +-- POST securetoken.googleapis.com ------>|  (refresh_token)
    |   /v1/token                              |
    |<-------- new idToken --------------------|
    |                                          |
    |  (idToken used as ?auth=<idToken> on     |
    |   all RTDB/Firestore requests)           |
```

Token refresh happens automatically when the token is within 5 minutes of expiry (tokens last ~60 minutes).

The web frontend supports two sign-in methods:
- **Google** -- via popup (recommended)
- **Email/Password** -- for manual accounts

### Service: `FirebaseService`

`web/src/app/services/firebase.service.ts`

All Firebase interaction is centralized in `FirebaseService`:

- **Auth**: `login`, `register`, `loginWithGoogle`, `logout`
- **Account linking**: `onUserLinkStatus(uid)`, `generateLinkCode(uid)`
- **Instance list**: `getInstances(uid)` -- Firestore query on `users/{uid}/instances`
- **Instance runtime**: `onInstanceRuntime(uid, instanceId)` -- RTDB listener on `users/{uid}/instances/{id}/runtime`
- **Streams**: `onStream(uid, sessionId)` -- RTDB listener on `users/{uid}/streams/{sessionId}`
- **Commands**: `sendCommand(uid, instanceId, action, payload)`, `sendCommandAndWait(...)` -- RTDB push to `users/{uid}/commands/{instanceId}`
- **History**: `getSessionHistory(uid, instanceId, sessionId)` -- Firestore query on `users/{uid}/instances/{id}/sessions/{sid}/messages`

### Interfaces

```typescript
interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  client_id?: string;
}

interface InstanceRuntime {
  online: boolean;
  client_id: string;
  last_seen: number;
}

interface StreamData {
  content: string;
  status: string;
  tool_calls: { name: string; status: string; detail: string }[];
  error?: string;
  updated_at: number;
}

interface Command {
  action: string;
  payload: unknown;
  status: string;
  user_id: string;
  created_at: number;
  updated_at: number;
  result?: unknown;
  error?: string;
}

interface HistoryMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  tool_calls: { name: string; status: string; detail: string; input?: string; output?: string }[];
  created_at: string;
}
```

### Components

| Component | File | Purpose |
|-----------|------|---------|
| `LoginComponent` | `web/src/app/components/login/` | Sign in via Google or email/password. Redirects to dashboard on success. |
| `DashboardComponent` | `web/src/app/components/dashboard/` | Main view. Shows account linking status, instance list (polled from Firestore every 5s), create/start/stop/delete controls. |
| `InstanceCardComponent` | `web/src/app/components/instance-card/` | Displays a single instance with status color, provider badge (CC/OC), and action buttons. |
| `PromptPanelComponent` | `web/src/app/components/prompt-panel/` | Session selector, message history (from Firestore), prompt input, real-time streaming display (from RTDB). Monitors command status for error detection with 30s timeout. |

### Routes

```
/login    --> LoginComponent
/         --> DashboardComponent (requires auth via authGuard)
/**       --> redirects to /
```

### Data Flows

#### Commands (Web --> Go)

```
Web: push(users/{uid}/commands/{instanceId})
  --> { action: "prompt", payload: {session_id, content}, status: "pending" }

Go (SSE on users/{uid}/commands):
  --> Detects new "pending" command
  --> PATCH status --> "ack"
  --> Execute handler
  --> PATCH status --> "done" (with result) or "error"

Web: onValue sees status change --> resolves promise / shows error
```

`sendCommandAndWait()` subscribes via `onValue` with a 30-second timeout.

`sendPrompt()` uses `sendCommand()` (fire-and-forget) but also monitors the command status. If the backend returns an error before the stream starts, the UI detects it immediately via the command status listener.

#### Real-time Streaming (Go --> Web)

```
Provider.Prompt() --> event channel
    --> Streamer intercepts events
    --> Buffers text + tool_calls in memory
    --> Flushes to users/{uid}/streams/{sessionId} every 300ms (PATCH)
    --> On done/error: final PATCH with terminal status

Web: onValue(users/{uid}/streams/{sessionId})
    --> Re-renders content on each update
```

#### Account Linking (Web + Telegram Bot)

Telegram account linking uses a 6-digit code flow:

1. Web dashboard generates a code and writes it to RTDB `link_codes/{code}` with the user's UID and a 10-minute expiry
2. User sends `/link <code>` to the Telegram bot
3. Bot reads `link_codes/{code}`, validates UID and expiry
4. Bot writes the Telegram user ID to `users/{uid}/telegram_id`
5. Bot deletes the link code
6. Web dashboard listens on `users/{uid}/telegram_id` to detect link status

---

## Comparison

| Aspect | Local Dashboard | Remote Web Frontend |
|--------|----------------|-------------------|
| **Communication** | Direct HTTP + SSE | Firebase RTDB + Firestore |
| **Latency** | Instant (~1ms) | ~100-300ms (Firebase relay) |
| **Auth** | None | Firebase Auth (Google/email) |
| **Requires Firebase** | No | Yes |
| **Remote access** | No (same machine only) | Yes (anywhere with internet) |
| **Multi-user** | No | Yes (user-scoped data) |
| **Streaming** | SSE via `/api/ws` | RTDB `onValue` listener |
| **Instance list** | HTTP poll (`/api/instances`) | Firestore poll (`getInstances`) |
| **Message history** | Not available | Firestore query |
| **Account linking** | N/A | 6-digit code via Telegram |
| **Build target** | `make dashboard` (embedded in binary) | `make web` (deployed to Firebase Hosting) |

---

## Security Rules

### Firestore (`firestore.rules`)

```
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid}/{document=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
```

### RTDB (`firebase-rules.json`)

```json
{
  "rules": {
    "users": {
      "$uid": {
        ".read": "auth != null && auth.uid === $uid",
        ".write": "auth != null && auth.uid === $uid"
      }
    },
    "link_codes": {
      "$code": {
        ".read": "auth != null",
        ".write": "auth != null"
      }
    }
  }
}
```

Both Go server and web frontend sign in as regular Firebase users. Security rules enforce that each user can only access their own data under `users/{uid}/`. The `link_codes` path is accessible to any authenticated user since the web frontend writes the code and a different Firebase user (the Go server) reads it.

The local dashboard does not use Firebase at all -- it communicates directly with the Go server, which runs on the same machine. There is no auth layer on the local dashboard; access control is handled at the OS/network level.

## Cost Estimate (Personal Use)

| Resource | Monthly Usage (est.) | Free Tier | Cost |
|----------|---------------------|-----------|------|
| Firebase Auth | <10 users | Unlimited | $0 |
| RTDB bandwidth | ~5 GB/month | 10 GB/month | $0 |
| RTDB storage | ~5 MB (ephemeral) | 1 GB | $0 |
| Firestore reads | ~100K/month | 50K/day | $0 |
| Firestore writes | ~10K/month | 20K/day | $0 |
| Firestore storage | ~50 MB | 1 GB | $0 |
| Local dashboard | Embedded in binary | N/A | $0 |
| **Total** | | | **$0** |
