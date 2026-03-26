# Refactoring Plan: File Splitting for Maintainability

## Problem

Several source files have grown too large, mixing multiple concerns in a single file. This makes navigation, code review, and maintenance harder than necessary.

**Files exceeding 400 lines (current state):**

| File | Lines | Concern Count |
|------|------:|:---:|
| `internal/bot/handlers.go` | 1,021 | 4 |
| `internal/bot/streaming.go` | 929 | 3 |
| `cmd/opencode-manager/main.go` | 859 | 4 |
| `internal/provider/claudecode.go` | 820 | 4 |
| `internal/store/firestore_store.go` | 488 | 2 |
| `internal/web/server.go` | 474 | 3 |
| `internal/process/manager.go` | 473 | 2 |
| `internal/bot/callbacks.go` | 452 | 1 |

## Approach

**File splitting within existing packages** — no package boundary changes, no interface changes, no behavioral changes. Each split is a pure move of functions/types to a new file in the same package. All existing tests continue to pass without modification.

This is not a rewrite. The goal is to bring every file under ~300 lines by extracting cohesive groups of functions into separate files.

---

## 1. `internal/bot/handlers.go` (1,021 lines) → 3 files

The worst offender. Currently mixes:
- Telegram command handlers (`/list`, `/switch`, `/stop`, etc.)
- Prompt flow (HandlePrompt, HandlePhoto, worktree choice, main-dir conflict, queue)
- File download utility
- Shared types and helpers

### Split plan:

**`handlers.go`** (~120 lines) — Types, constructor, shared helper
- `pendingPrompt` struct
- `Handlers` struct
- `NewHandlers()`
- `getActiveInstance()`

**`commands.go`** (~400 lines) — Pure command handlers
- `HandleLink`, `HandleStart`, `HandleHelp`
- `HandleNew`, `HandleNewOpenCode`, `handleNewInstance`
- `HandleList`, `HandleSwitch`, `HandleStop`, `HandleStartInst`
- `HandleStatus`, `HandleSession`, `HandleSessions`, `HandleAbort`

**`prompt.go`** (~500 lines) — Prompt flow + photo handling
- `HandlePrompt`, `HandlePhoto`
- `buildPhotoPrompt`, `downloadTelegramFile`
- `showWorktreeChoice`, `createSessionAndPrompt`, `startPrompt`
- `showMainDirConflict`, `queueMainDirPrompt`

> `commands.go` is still ~400 lines because each command handler is small but there are 14 of them. Further splitting (e.g. `commands_instance.go` + `commands_session.go`) is possible but adds marginal value — the file is easy to navigate with a flat list of `Handle*` functions.

---

## 2. `internal/bot/streaming.go` (929 lines) → 3 files

Currently mixes three distinct subsystems in one file:
- Individual stream lifecycle (`StreamContext`)
- Stream manager (`StreamManager`)
- Active Tasks board (rendering + refresh loop)

### Split plan:

**`stream_context.go`** (~350 lines) — Individual stream lifecycle
- `toolStatus`, `StreamContext` struct
- `AddCleanupFile`, `OnDone`, `Result`, `MarkSuperseded`
- `consumeStream`, `updateTool`, `flushLoop`, `isDone`
- `sendResponse`, `sendSingleMessage`, `sendSplitResponse`, `sendAsFile`
- `mergeBack`, `sendMergeNotification`, `hasMergeError`, `sendMergeFailureNotification`
- `cleanup`

**`stream_manager.go`** (~120 lines) — Stream orchestrator
- `StreamManager` struct
- `NewStreamManager`, `StartStream`
- `RemoveStream`, `StopTask`, `NotifyNewMessage`, `onStreamDone`

**`board.go`** (~250 lines) — Active Tasks board
- `boardEntry` struct
- `boardLoop`, `refreshBoard`
- `buildBoardHTML`, `boardKeyboard`
- `toolStateIcon`, `formatElapsed`

---

## 3. `cmd/opencode-manager/main.go` (859 lines) → 4 files

The entry point currently contains the full login wizard, config resolution, migration, and many CLI helpers alongside the main serve loop.

### Split plan:

**`main.go`** (~180 lines) — Entry point + serve loop
- `credentialsFile` struct
- `main()` (dispatch: login, relogin, serve)
- `runServe()` (signal handling, Firebase init, config load, app start)

**`login.go`** (~250 lines) — Interactive login wizard
- `loginResult`, `firebaseProjectConfig` structs
- `runLogin()` (4-step setup)
- `runRelogin()`
- `doBrowserLogin()` (local HTTP server for OAuth)
- `reloginCredentials()`

**`setup.go`** (~250 lines) — Config resolution + Firebase setup
- `resolveFirebaseProjectConfig`, `projectConfigFromCredentials`, `deriveProjectID`
- `newFirebaseClient`, `newFirestoreAdapter`
- `ensureClientID`, `maybeRecoverFirebaseCredentials`, `shouldOfferRelogin`, `isInteractiveTerminal`
- `migrateFromRTDB`

**`helpers.go`** (~100 lines) — CLI UI utilities
- `readCredentials`, `writeCredentials`
- `openBrowser`, `detectBinary`
- `promptWithDefault`, `maskToken`
- `printLoginStep`, `printOK`, `printFail`

---

## 4. `internal/provider/claudecode.go` (820 lines) → 4 files

Mixes core provider logic, git worktree management, main-dir locking, and a substantial JSON stream parser.

### Split plan:

**`claudecode.go`** (~280 lines) — Core provider
- `sessionCmd`, `ClaudeCodeProvider` struct
- `NewClaudeCodeProvider`
- `Type`, `Start`, `Stop`, `WaitReady`, `Wait`, `Stderr`, `SetPort`, `IsReady`, `HealthCheck`
- `CreateSession`, `GetSession`, `ListSessions`
- `Prompt`, `Abort`, `DeleteSession`

**`claudecode_worktree.go`** (~200 lines) — Git worktree management
- `isGitRepo`, `currentBranch`
- `createWorktree`, `removeWorktree`
- `mergeAndSync`, `syncWorktrees`
- `enforceWorktreeLimit`
- `SupportsWorktree`

**`claudecode_maindir.go`** (~50 lines) — Main-dir exclusive locking
- `IsMainDirBusy`, `TryAcquireMainDir`
- `WaitMainDirFree`, `ReleaseMainDir`

**`claudecode_parser.go`** (~290 lines) — Stream-JSON event parsing
- `claudeEvent`, `claudeStreamEvent`, `claudeDelta`, `claudeMessage`, `claudeBlock`, `claudeTool` type structs
- `claudeParser` struct
- `parseEvent`, `appendText`, `resetText`
- `extractToolDetail`, `extractToolDetailFromMap`, `extractDetailFromInputBuf`
- `shortenPath`

---

## 5. `internal/web/server.go` (474 lines) → 3 files

Mixes HTTP server lifecycle, REST API handlers, and the SSE streaming hub.

### Split plan:

**`server.go`** (~130 lines) — Server lifecycle
- `Server` struct, `NewServer`
- `Start`, `Stop`, `Hub`, `SetDevProxy`
- `corsMiddleware`, `writeJSON`, `writeError`

**`api.go`** (~200 lines) — REST API handlers
- `instanceJSON` struct
- `handleInstances`, `handleInstanceDetail`
- `handleSessions`, `handlePrompt`, `handleAbort`

**`hub.go`** (~130 lines) — SSE StreamHub
- `StreamHub`, `wsClient` structs
- `NewStreamHub`, `Run`, `Broadcast`
- `HandleWebSocket`

---

## 6. Lower Priority Splits

These files are borderline (400-490 lines) and well-organized internally. Splitting is optional but would bring consistency.

### `internal/store/firestore_store.go` (488 lines) → 2 files

**`firestore_store.go`** (~310 lines) — CRUD methods + path helpers

**`firestore_helpers.go`** (~180 lines) — Serialization helpers
- `getString`, `getInt`, `getBool`, `parseTimestamp`
- `docToInstance`, `docToSession`

### `internal/process/manager.go` (473 lines) → 2 files

**`manager.go`** (~300 lines) — Core lifecycle (Create, Start, Stop, Delete, List, Shutdown)

**`manager_recovery.go`** (~170 lines) — Crash recovery + instance restoration
- `watchInstance`, `RestoreInstances`, `LoadStopped`, `StartHealthChecks`

### `internal/bot/callbacks.go` (452 lines) — Keep as is

This file has a single clear responsibility (callback dispatch + handlers). Each handler is small. The length comes from having many independent handlers. No split needed.

---

## Execution Order

Each step is an independent, self-contained commit. No step depends on another.

| Step | Files Affected | Risk | Estimated Lines Moved |
|------|---------------|------|----------------------|
| 1 | `bot/streaming.go` → 3 files | Low | 929 |
| 2 | `provider/claudecode.go` → 4 files | Low | 820 |
| 3 | `cmd/main.go` → 4 files | Low | 859 |
| 4 | `bot/handlers.go` → 3 files | Low | 1,021 |
| 5 | `web/server.go` → 3 files | Low | 474 |
| 6 | `store/firestore_store.go` → 2 files | Low | 488 |
| 7 | `process/manager.go` → 2 files | Low | 473 |

All steps are pure file moves within the same package. No import changes, no interface changes, no logic changes.

---

## Post-Split File Size Target

After refactoring, no file should exceed ~350 lines. Target distribution:

```
cmd/opencode-manager/
  main.go          ~180 lines
  login.go         ~250 lines
  setup.go         ~250 lines
  helpers.go       ~100 lines

internal/bot/
  bot.go           ~161 lines  (unchanged)
  handlers.go      ~120 lines
  commands.go      ~400 lines  (many small handlers)
  prompt.go        ~500 lines  (complex prompt flow — acceptable)
  callbacks.go     ~452 lines  (unchanged, many small handlers)
  stream_context.go ~350 lines
  stream_manager.go ~120 lines
  board.go         ~250 lines
  format.go        ~320 lines  (unchanged)
  keyboard.go      ~131 lines  (unchanged)

internal/provider/
  provider.go       ~104 lines  (unchanged)
  claudecode.go     ~280 lines
  claudecode_worktree.go ~200 lines
  claudecode_maindir.go   ~50 lines
  claudecode_parser.go   ~290 lines
  opencode.go       ~335 lines  (unchanged)

internal/web/
  server.go         ~130 lines
  api.go            ~200 lines
  hub.go            ~130 lines
  devproxy.go       ~136 lines  (unchanged)

internal/store/
  iface.go              ~105 lines  (unchanged)
  firestore_store.go    ~310 lines
  firestore_helpers.go  ~180 lines
  firestore_adapter.go   ~47 lines  (unchanged)

internal/process/
  manager.go          ~300 lines
  manager_recovery.go ~170 lines
  instance.go          ~42 lines  (unchanged)
  portpool.go          ~57 lines  (unchanged)
```

## What This Does NOT Change

- No package boundaries are moved
- No interfaces are modified
- No function signatures change
- No logic changes
- No new dependencies
- All existing tests pass without modification
- `go build` / `go vet` / `golangci-lint` clean after each step

## Validation

After each step:
```bash
go build ./...
go vet ./...
go test ./...
make lint  # golangci-lint
```
