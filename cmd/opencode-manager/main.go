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

	"github.com/pufanyi/opencode-manager/internal/app"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/setup"
	"github.com/pufanyi/opencode-manager/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
		return
	}

	runServe()
}

func getDBPath() string {
	// Check flag
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	dbFlag := fs.String("db", "", "path to database file")
	_ = fs.String("config", "", "deprecated: config is now stored in database")
	_ = fs.Bool("dev", false, "")
	_ = fs.Parse(os.Args[1:])
	if *dbFlag != "" {
		return *dbFlag
	}

	// Check env
	if v := os.Getenv("STORAGE_DATABASE"); v != "" {
		return v
	}

	// Default
	return "./data/opencode-manager.db"
}

func openStore(dbPath string) (*store.Store, error) {
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	return store.New(dbPath)
}

func runSetup() {
	dbPath := getDBPath()

	st, err := openStore(dbPath)
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
	dbPath := flag.String("db", "", "path to database file")
	devMode := flag.Bool("dev", false, "enable dev mode with Angular dev server (HMR)")
	// Keep -config for backwards compatibility message
	configPath := flag.String("config", "", "deprecated: config is now stored in database")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opencode-manager [command] [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  setup    Interactive setup wizard\n")
		fmt.Fprintf(os.Stderr, "  (none)   Start the manager (default)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *configPath != "" {
		fmt.Fprintln(os.Stderr, "Warning: -config flag is deprecated. Config is now stored in the database.")
		fmt.Fprintln(os.Stderr, "Run 'opencode-manager setup' to migrate your settings.")
	}

	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Determine DB path
	dp := *dbPath
	if dp == "" {
		dp = os.Getenv("STORAGE_DATABASE")
	}
	if dp == "" {
		dp = "./data/opencode-manager.db"
	}

	st, err := openStore(dp)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}

	// Check if settings exist; if not, run interactive setup
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

	// Load config from DB
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

	// Create application
	application, err := app.New(cfg, st, *devMode)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		st.Close()
		os.Exit(1)
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal", "signal", sig)
		cancel()
		application.Shutdown()
	}()

	slog.Info("starting opencode-manager", "db", dp)

	if err := application.Start(ctx); err != nil {
		cancel()
		application.Shutdown()
		slog.Error("application error", "error", err)
	}
}
