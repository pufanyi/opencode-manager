package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pufanyi/opencode-manager/internal/app"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// credentialsFile is the minimal local config — only Firebase connection info.
type credentialsFile struct {
	Firebase struct {
		APIKey       string `yaml:"api_key"`
		ServerAPIKey string `yaml:"server_api_key,omitempty"`
		DatabaseURL  string `yaml:"database_url"`
		AuthDomain   string `yaml:"auth_domain,omitempty"`
		ProjectID    string `yaml:"project_id,omitempty"`
		Email        string `yaml:"email,omitempty"`
		Password     string `yaml:"password,omitempty"`
		RefreshToken string `yaml:"refresh_token,omitempty"`
	} `yaml:"firebase"`
	ClientID string `yaml:"client_id,omitempty"`
}

// Default Firebase project values (from environment.ts).
const (
	defaultAPIKey       = "AIzaSyCECBGZeLmLdi2a8Viii7iIoYksLKlDPPY" // browser key (with referrer restrictions)
	defaultServerAPIKey = "AIzaSyByIo86in28AaxA6g8X9aOCIzKBAzF1vek" // server key (no referrer restrictions)
	defaultDBURL        = "https://opencode-manager-default-rtdb.firebaseio.com"
	defaultAuthDom      = "opencode-manager.firebaseapp.com"
	defaultProjID       = "opencode-manager"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin()
			return
		case "relogin":
			runRelogin()
			return
		}
	}

	runServe()
}

func runServe() {
	credPath := flag.String("credentials", "./credentials.yaml", "path to Firebase credentials file")
	devMode := flag.Bool("dev", false, "enable dev mode with Angular dev server (HMR)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: opencode-manager [command] [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  login    Browser login + interactive cloud setup\n")
		fmt.Fprintf(os.Stderr, "  relogin  Refresh Firebase browser credentials in credentials.yaml\n")
		fmt.Fprintf(os.Stderr, "  (none)   Start the manager (default)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Setup structured logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
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

	// Auto-generate client_id on first run.
	ensureClientID(creds, *credPath)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Cancel context on signal so blocking calls (e.g. WaitForConfig) can exit.
	go func() {
		sig := <-sigCh
		slog.Info("received signal", "signal", sig)
		cancel()
	}()

	slog.Info("connecting to Firebase...", "project", creds.Firebase.DatabaseURL)

	fbClient, err := newFirebaseClient(creds)
	if err != nil {
		if nextCreds, recovered := maybeRecoverFirebaseCredentials(*credPath, creds, err); recovered {
			creds = nextCreds
			ensureClientID(creds, *credPath)
			fbClient, err = newFirebaseClient(creds)
		}
	}
	if err != nil {
		slog.Error("firebase connection failed", "error", err)
		os.Exit(1)
	}

	// Create Firestore store for persistent data.
	if fbClient.Firestore == nil {
		slog.Error("Firestore not available (ProjectID is required)")
		os.Exit(1)
	}
	st := store.NewFirestoreStore(ctx, newFirestoreAdapter(fbClient), fbClient.UID())
	slog.Info("using Firestore for persistent storage", "uid", fbClient.UID(), "client_id", fbClient.ClientID())

	// Pull config from Firestore (user-level + client-level).
	userConfig, err := st.GetUserConfig()
	if err != nil {
		if nextCreds, recovered := maybeRecoverFirebaseCredentials(*credPath, creds, err); recovered {
			creds = nextCreds
			ensureClientID(creds, *credPath)
			fbClient, err = newFirebaseClient(creds)
			if err == nil {
				st = store.NewFirestoreStore(ctx, newFirestoreAdapter(fbClient), fbClient.UID())
				userConfig, err = st.GetUserConfig()
			}
		}
	}
	if err != nil {
		slog.Error("failed to pull user config from Firestore", "error", err)
		os.Exit(1)
	}

	clientConfig, _ := st.GetClientConfig(fbClient.ClientID())

	if len(userConfig) == 0 {
		// Try to migrate config from legacy RTDB /config.
		userConfig, clientConfig = migrateFromRTDB(ctx, fbClient, st, creds.ClientID)
		if len(userConfig) == 0 {
			slog.Info("no config found — run 'login' to set up configuration")
			os.Exit(1)
		}
	}

	slog.Info("config loaded from Firestore", "user_keys", len(userConfig), "client_keys", len(clientConfig))

	// Build config from Firestore settings.
	cfg := config.LoadFromSettings(userConfig, clientConfig)
	config.ApplyEnvOverrides(cfg)

	// Force Firebase enabled with credentials from file.
	cfg.Firebase.Enabled = true
	cfg.Firebase.APIKey = creds.Firebase.APIKey
	cfg.Firebase.ServerAPIKey = creds.Firebase.ServerAPIKey
	cfg.Firebase.DatabaseURL = creds.Firebase.DatabaseURL
	cfg.Firebase.RefreshToken = creds.Firebase.RefreshToken
	cfg.Firebase.Email = creds.Firebase.Email
	cfg.Firebase.Password = creds.Firebase.Password
	if cfg.Firebase.ProjectID == "" {
		cfg.Firebase.ProjectID = creds.Firebase.ProjectID
		if cfg.Firebase.ProjectID == "" {
			cfg.Firebase.ProjectID = deriveProjectID(creds.Firebase.DatabaseURL)
		}
	}

	if err := config.Validate(cfg); err != nil {
		slog.Error("config validation failed", "error", err)
		os.Exit(1)
	}

	// Create and start application.
	application, err := app.New(cfg, st, fbClient, *devMode)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		st.Close()
		os.Exit(1)
	}

	// Re-register for a second signal to also shut down the application.
	sigCh2 := make(chan os.Signal, 1)
	signal.Notify(sigCh2, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh2
		slog.Info("received signal", "signal", sig)
		cancel()
		application.Shutdown()
	}()

	slog.Info("starting opencode-manager (cloud mode)")

	if err := application.Start(ctx); err != nil {
		cancel()
		application.Shutdown()
		slog.Error("application error", "error", err)
	}

	st.Close()
}
