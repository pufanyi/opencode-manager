# Desktop App Design (Wails)

## Goal

Add a `desktop` subcommand so the binary can run as a native desktop application with the Angular frontend in a system webview window, while all backend services (Telegram bot, process manager, Firebase) continue running in the background.

```
opencode-manager           # CLI mode (existing, headless)
opencode-manager desktop   # Desktop mode (native window + backend)
```

## Why Wails

The Angular frontend already communicates entirely through Firebase (Auth, RTDB, Firestore) — zero HTTP calls to the Go backend. Wails simply embeds the same Angular build in a native window. No frontend changes required.

| Alternative | Why Not |
|---|---|
| Electron | Bundles Chromium (~150MB), needs Node.js runtime |
| Tauri | Rust-based, doesn't fit a Go codebase |
| Fyne | Pure-Go UI toolkit, but we already have a full Angular frontend |
| **Wails v2** | **Go-native, uses system webview, embeds existing web assets** |

## Architecture

### CLI Mode (existing, unchanged)

```
main() → runServe()
  → Firebase connect
  → App.Start()
    → restore instances, start Firebase background services
    → start web server (optional)
    → bot.Start(ctx)  ← blocking
```

### Desktop Mode (new)

```
main() → runDesktop()
  → Firebase connect
  → App.StartBackground()   ← new method, non-blocking
    → restore instances, start Firebase background services
    → bot.Start(ctx) as goroutine  ← NOT blocking
  → wails.Run()  ← blocking (native window event loop)
    → embedded Angular frontend
    → Angular talks to Firebase (unchanged)
  → on window close: App.Shutdown()
```

### Key Difference

`App.Start()` currently blocks on `a.bot.Start(ctx)`. Desktop mode needs a non-blocking variant where the Telegram bot runs as a goroutine, so the Wails event loop can be the blocking call.

```go
// New method on App:
func (a *App) StartBackground(ctx context.Context) error {
    // Same setup as Start() but bot runs as goroutine
    // ... register client, restore instances, start Firebase ...
    go a.bot.Start(ctx)  // non-blocking
    return nil
}
```

## Build Strategy

Wails requires CGo (webview bindings). Use **build tags** to keep CLI builds CGo-free:

```
go build ./cmd/opencode-manager                    # CLI (no CGo, no Wails)
go build -tags desktop ./cmd/opencode-manager      # Desktop (CGo, Wails)
```

### File Layout

```
cmd/opencode-manager/
├── main.go               # Entry point, dispatches login/relogin/serve/desktop
├── login.go              # Login wizard (unchanged)
├── setup.go              # Firebase setup (unchanged)
├── helpers.go            # CLI utilities (unchanged)
├── desktop.go            # //go:build desktop — runDesktop(), Wails setup
└── desktop_stub.go       # //go:build !desktop — stub that prints "build with -tags desktop"
```

### `desktop.go` (build tag: `desktop`)

```go
//go:build desktop

package main

import (
    "embed"
    "github.com/wailsapp/wails/v2"
    "github.com/wailsapp/wails/v2/pkg/options"
    "github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:desktop-dist
var desktopAssets embed.FS

func runDesktop() {
    // 1. Read credentials, connect Firebase, build config (same as runServe)
    // 2. Create App
    // 3. app.StartBackground(ctx) — non-blocking
    // 4. Start Wails window

    err := wails.Run(&options.App{
        Title:  "OpenCode Manager",
        Width:  1200,
        Height: 800,
        AssetServer: &assetserver.Options{
            Assets: desktopAssets,
        },
        OnShutdown: func(ctx context.Context) {
            application.Shutdown()
        },
        Bind: []interface{}{
            // Optional: expose Go methods directly to JS
            // For now, the frontend uses Firebase — no bindings needed
        },
    })
}
```

### `desktop_stub.go` (build tag: `!desktop`)

```go
//go:build !desktop

package main

import (
    "fmt"
    "os"
)

func runDesktop() {
    fmt.Fprintln(os.Stderr, "Desktop mode not available. Rebuild with: go build -tags desktop ./cmd/opencode-manager")
    os.Exit(1)
}
```

### `main.go` Change

```go
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "login":
            runLogin()
            return
        case "relogin":
            runRelogin()
            return
        case "desktop":
            runDesktop()
            return
        }
    }
    runServe()
}
```

## Frontend Assets

The desktop build embeds the same Angular build, but from a different embed path to avoid conflicts with the web server's embed:

```
# Build for desktop (same Angular build, different embed location)
cd web && pnpm ng build --output-path ../cmd/opencode-manager/desktop-dist
```

Or symlink/copy from `internal/web/dist` to `cmd/opencode-manager/desktop-dist` in the Makefile.

### Makefile Additions

```makefile
desktop: web
	cp -r internal/web/dist cmd/opencode-manager/desktop-dist
	go build -tags desktop -o $(BUILD_DIR)/$(BINARY)-desktop ./cmd/opencode-manager
	rm -rf cmd/opencode-manager/desktop-dist
```

## App Changes

### `internal/app/app.go`

Add a non-blocking start method:

```go
// StartBackground starts all services without blocking.
// The Telegram bot runs as a goroutine. Use this for desktop mode
// where the Wails event loop is the blocking call.
func (a *App) StartBackground(ctx context.Context) error {
    // Same setup as Start()...
    // register client, restore instances, start health checks,
    // start Firebase background, start web dashboard...

    // Bot runs in background (non-blocking)
    go a.bot.Start(ctx)

    return nil
}
```

The existing `Start()` method remains unchanged (blocking on bot).

## Optional: Wails Go Bindings

The frontend currently works via Firebase. But Wails allows binding Go methods directly to JS, which could be used for:

1. **Local-only operations** — things that don't need Firebase round-trips
2. **Offline mode** — basic functionality without Firebase
3. **System integration** — file dialogs, notifications, tray icon

These are optional future enhancements. The initial desktop version needs zero bindings.

### Example Future Binding

```go
// DesktopAPI exposes Go methods to the Wails frontend
type DesktopAPI struct {
    app *App
}

func (d *DesktopAPI) ListInstances() []instanceInfo { ... }
func (d *DesktopAPI) OpenProjectFolder(instanceID string) error { ... }
func (d *DesktopAPI) GetSystemInfo() map[string]string { ... }
```

## System Requirements (Desktop Build)

### Build Dependencies

| OS | Package |
|---|---|
| Linux | `libgtk-3-dev`, `libwebkit2gtk-4.0-dev` |
| macOS | Xcode command line tools (WebKit is built-in) |
| Windows | WebView2 runtime (auto-installed, ships with Windows 11) |

### Runtime Dependencies

| OS | Requirement |
|---|---|
| Linux | GTK3, WebKit2GTK 4.0+ |
| macOS | macOS 10.15+ (Catalina) |
| Windows | WebView2 runtime (Windows 10 1803+) |

## What Changes vs What Doesn't

### Changes

| Component | Change |
|---|---|
| `cmd/opencode-manager/main.go` | Add `desktop` case to command dispatch |
| `cmd/opencode-manager/desktop.go` | New file (build tag `desktop`) |
| `cmd/opencode-manager/desktop_stub.go` | New file (build tag `!desktop`) |
| `internal/app/app.go` | Add `StartBackground()` method |
| `Makefile` | Add `desktop` target |
| `go.mod` | Add `github.com/wailsapp/wails/v2` (only used with tag) |

### Unchanged

| Component | Reason |
|---|---|
| Angular frontend | Already uses Firebase, no modifications needed |
| Telegram bot | Still runs (as goroutine in desktop mode) |
| Firebase layer | Unchanged |
| Process manager | Unchanged |
| Store layer | Unchanged |
| CLI mode | Unchanged, no CGo dependency added |
| Web server | Still available (can run alongside desktop window) |

## Execution Order

1. Add `StartBackground()` to `internal/app/app.go`
2. Add `desktop` case to `main.go`
3. Create `desktop_stub.go` (always compiles, prints error)
4. Create `desktop.go` (compiles only with `-tags desktop`)
5. Add Makefile target
6. Add `wails/v2` dependency
7. Test CLI build still works without CGo
8. Test desktop build
