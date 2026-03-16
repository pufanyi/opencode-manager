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
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		runSetup()
		return
	}

	runServe()
}

func runSetup() {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	output := fs.String("output", "", "output config file path (default: opencode-manager.yaml)")
	_ = fs.Parse(os.Args[2:])

	if err := setup.Run(*output); err != nil {
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}
}

func runServe() {
	configPath := flag.String("config", "", "path to config file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opencode-manager [command] [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  setup    Interactive setup wizard to generate config\n")
		fmt.Fprintf(os.Stderr, "  (none)   Start the manager (default)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Find config file
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("OPENCODE_MANAGER_CONFIG")
	}
	if cfgPath == "" {
		candidates := []string{
			"opencode-manager.yaml",
			"configs/opencode-manager.yaml",
			filepath.Join(os.Getenv("HOME"), ".config", "opencode-manager", "opencode-manager.yaml"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				cfgPath = c
				break
			}
		}
	}

	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "No config file found.")
		fmt.Fprintln(os.Stderr, "Run 'opencode-manager setup' to create one, or use -config flag.")
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Ensure data directory exists
	dbDir := filepath.Dir(cfg.Storage.Database)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		slog.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}

	// Create application
	application, err := app.New(cfg)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
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

	slog.Info("starting opencode-manager", "config", cfgPath)

	if err := application.Start(ctx); err != nil {
		slog.Error("application error", "error", err)
		os.Exit(1)
	}
}
