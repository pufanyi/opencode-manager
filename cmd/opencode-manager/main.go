package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/pufanyi/opencode-manager/internal/app"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/setup"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// credentialsFile is the minimal local config — only Firebase connection info.
type credentialsFile struct {
	Firebase struct {
		APIKey      string `yaml:"api_key"`
		DatabaseURL string `yaml:"database_url"`
		Email       string `yaml:"email"`
		Password    string `yaml:"password"`
	} `yaml:"firebase"`
	DBPath string `yaml:"db_path"` // optional, defaults to ./data/opencode-manager.db
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
		return
	}

	runServe()
}

func readCredentials(path string) (*credentialsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds credentialsFile
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &creds, nil
}

func openStore(dbPath string) (*store.Store, error) {
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	return store.New(dbPath)
}

func getDBPath(creds *credentialsFile, flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("STORAGE_DATABASE"); v != "" {
		return v
	}
	if creds != nil && creds.DBPath != "" {
		return creds.DBPath
	}
	return "./data/opencode-manager.db"
}

// runSetup is the legacy interactive setup wizard (writes to local SQLite).
func runSetup() {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	dbFlag := fs.String("db", "", "path to database file")
	_ = fs.Parse(os.Args[2:])

	dp := getDBPath(nil, *dbFlag)
	st, err := openStore(dp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := setup.Run(st); err != nil {
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}
}

func runServe() {
	credPath := flag.String("credentials", "./credentials.yaml", "path to Firebase credentials file")
	dbPathFlag := flag.String("db", "", "path to local database file (optional)")
	devMode := flag.Bool("dev", false, "enable dev mode with Angular dev server (HMR)")
	legacyMode := flag.Bool("legacy", false, "use local SQLite config instead of Firebase (backward compat)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opencode-manager [command] [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  setup    Interactive setup wizard (legacy, local config)\n")
		fmt.Fprintf(os.Stderr, "  (none)   Start the manager (default)\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  Default:  Config from Firebase (needs credentials.yaml)\n")
		fmt.Fprintf(os.Stderr, "  --legacy: Config from local SQLite database\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Setup structured logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if *legacyMode {
		runLegacy(ctx, cancel, sigCh, *dbPathFlag, *devMode)
		return
	}

	// ── Cloud-first boot ────────────────────────────────────────────────
	creds, err := readCredentials(*credPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Error("credentials file not found",
				"path", *credPath,
				"hint", "copy credentials.yaml.example to credentials.yaml and fill in your Firebase project info")
		} else {
			slog.Error("failed to read credentials", "error", err)
		}
		os.Exit(1)
	}

	slog.Info("connecting to Firebase...", "project", creds.Firebase.DatabaseURL)

	fbClient, err := firebase.NewClient(firebase.Config{
		APIKey:      creds.Firebase.APIKey,
		DatabaseURL: creds.Firebase.DatabaseURL,
		Email:       creds.Firebase.Email,
		Password:    creds.Firebase.Password,
	})
	if err != nil {
		slog.Error("firebase connection failed", "error", err)
		os.Exit(1)
	}

	// Pull config from Firebase.
	remoteConfig := firebase.NewRemoteConfig(fbClient.RTDB)
	settings, err := remoteConfig.Pull(ctx)
	if err != nil {
		slog.Error("failed to pull config from Firebase", "error", err)
		os.Exit(1)
	}

	if settings == nil || len(settings) == 0 {
		slog.Info("no config found in Firebase — waiting for web frontend setup...")
		slog.Info("open the web frontend and configure Telegram token, allowed users, etc.")
		settings, err = remoteConfig.WaitForConfig(ctx)
		if err != nil {
			slog.Error("waiting for config failed", "error", err)
			os.Exit(1)
		}
	}

	slog.Info("config loaded from Firebase", "keys", len(settings))

	// Build config from remote settings.
	cfg := config.LoadFromSettings(settings)
	config.ApplyEnvOverrides(cfg)

	// Force Firebase enabled with credentials from file.
	cfg.Firebase.Enabled = true
	cfg.Firebase.APIKey = creds.Firebase.APIKey
	cfg.Firebase.DatabaseURL = creds.Firebase.DatabaseURL
	cfg.Firebase.Email = creds.Firebase.Email
	cfg.Firebase.Password = creds.Firebase.Password

	if err := config.Validate(cfg); err != nil {
		slog.Error("config validation failed", "error", err)
		os.Exit(1)
	}

	// Open local SQLite for state cache (instances, sessions).
	dp := getDBPath(creds, *dbPathFlag)
	st, err := openStore(dp)
	if err != nil {
		slog.Error("failed to open local database", "error", err)
		os.Exit(1)
	}

	// Sync remote settings to local DB cache.
	_ = st.SetSettings(settings)

	// Create and start application.
	application, err := app.New(cfg, st, *devMode)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		st.Close()
		os.Exit(1)
	}

	go func() {
		sig := <-sigCh
		slog.Info("received signal", "signal", sig)
		cancel()
		application.Shutdown()
	}()

	slog.Info("starting opencode-manager (cloud mode)", "db", dp)

	if err := application.Start(ctx); err != nil {
		cancel()
		application.Shutdown()
		slog.Error("application error", "error", err)
	}

	st.Close()
}

// runLegacy is the original boot path: config from local SQLite.
func runLegacy(ctx context.Context, cancel context.CancelFunc, sigCh <-chan os.Signal, dbPathFlag string, devMode bool) {
	dp := getDBPath(nil, dbPathFlag)

	st, err := openStore(dp)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}

	hasSettings, err := st.HasSettings()
	if err != nil {
		slog.Error("failed to check settings", "error", err)
		os.Exit(1)
	}

	if !hasSettings {
		fmt.Println("No configuration found. Running setup wizard...")
		fmt.Println()
		if err := setup.Run(st); err != nil {
			slog.Error("setup failed", "error", err)
			st.Close()
			os.Exit(1)
		}
	}

	settings, err := st.GetAllSettings()
	if err != nil {
		slog.Error("failed to load settings", "error", err)
		st.Close()
		os.Exit(1)
	}

	cfg := config.LoadFromSettings(settings)
	config.ApplyEnvOverrides(cfg)

	if err := config.Validate(cfg); err != nil {
		slog.Error("config validation failed", "error", err)
		st.Close()
		os.Exit(1)
	}

	application, err := app.New(cfg, st, devMode)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		st.Close()
		os.Exit(1)
	}

	go func() {
		sig := <-sigCh
		slog.Info("received signal", "signal", sig)
		cancel()
		application.Shutdown()
	}()

	slog.Info("starting opencode-manager (legacy mode)", "db", dp)

	if err := application.Start(ctx); err != nil {
		cancel()
		application.Shutdown()
		slog.Error("application error", "error", err)
	}

	st.Close()
}
