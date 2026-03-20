package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin()
			return
		case "setup":
			runSetup()
			return
		}
	}

	runServe()
}

func runLogin() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("  \033[1m\033[36m┌──────────────────────────────────────┐\033[0m")
	fmt.Println("  \033[1m\033[36m│     OpenCode Manager Login           │\033[0m")
	fmt.Println("  \033[1m\033[36m└──────────────────────────────────────┘\033[0m")
	fmt.Println()

	// Determine output path.
	credPath := "./credentials.yaml"
	if len(os.Args) > 2 {
		credPath = os.Args[2]
	}

	// Check if credentials already exist.
	if _, err := os.Stat(credPath); err == nil {
		fmt.Printf("  \033[33m⚠ %s already exists. Overwrite? [y/N]: \033[0m", credPath)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("  Aborted.")
			return
		}
		fmt.Println()
	}

	// Default Firebase project values (from environment.ts).
	const defaultAPIKey = "AIzaSyCECBGZeLmLdi2a8Viii7iIoYksLKlDPPY"
	const defaultDBURL = "https://opencode-manager-default-rtdb.firebaseio.com"

	// Credentials.
	fmt.Println("  \033[1mGo Server Account\033[0m")
	fmt.Println("  \033[33mThe email/password for the Go server user in Firebase Auth.\033[0m")
	fmt.Println()

	email := promptInput(reader, "  Email: ")
	password := promptPassword(reader, "  Password: ")
	fmt.Println()

	apiKey := defaultAPIKey
	dbURL := defaultDBURL

	// Verify by signing in.
	fmt.Print("  Verifying credentials... ")
	auth := firebase.NewAuth(apiKey)
	if err := auth.SignIn(email, password); err != nil {
		fmt.Printf("\033[31m✗ %s\033[0m\n", err)
		fmt.Println()
		fmt.Println("  \033[33mCheck your API key, database URL, and credentials.\033[0m")
		fmt.Println("  \033[33mMake sure the user exists in Firebase Console → Authentication → Users.\033[0m")
		os.Exit(1)
	}
	fmt.Println("\033[32m✓ Signed in successfully\033[0m")
	fmt.Println()

	// Write credentials file.
	content := fmt.Sprintf(`firebase:
  api_key: %q
  database_url: %q
  email: %q
  password: %q
`, apiKey, dbURL, email, password)

	if err := os.WriteFile(credPath, []byte(content), 0600); err != nil {
		fmt.Printf("  \033[31m✗ Failed to write %s: %v\033[0m\n", credPath, err)
		os.Exit(1)
	}

	fmt.Printf("  \033[32m✓ Credentials saved to %s\033[0m\n", credPath)
	fmt.Println()
	fmt.Println("  \033[1mNext steps:\033[0m")
	fmt.Println()
	fmt.Printf("  1. Add config to Firebase RTDB (telegram.token, telegram.allowed_users)\n")
	fmt.Printf("  2. Run: \033[36m./bin/opencode-manager\033[0m\n")
	fmt.Println()
}

func promptInput(r *bufio.Reader, msg string) string {
	fmt.Print(msg)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptPassword(r *bufio.Reader, msg string) string {
	// Try to disable echo (best effort, falls back to plain input).
	fmt.Print(msg)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
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

	if err := setup.Run(st); err != nil {
		st.Close()
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}
	st.Close()
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

	if *legacyMode {
		ctx, cancel := context.WithCancel(context.Background())
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		runLegacy(ctx, cancel, sigCh, *dbPathFlag, *devMode)
		cancel()
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

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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

	if len(settings) == 0 {
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
	if err := st.SetSettings(settings); err != nil {
		slog.Warn("failed to cache settings locally", "error", err)
	}

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
