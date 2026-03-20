package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/pufanyi/opencode-manager/internal/app"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/firebase/loginpage"
	"github.com/pufanyi/opencode-manager/internal/setup"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// credentialsFile is the minimal local config — only Firebase connection info.
type credentialsFile struct {
	Firebase struct {
		APIKey       string `yaml:"api_key"`
		DatabaseURL  string `yaml:"database_url"`
		AuthDomain   string `yaml:"auth_domain,omitempty"`
		ProjectID    string `yaml:"project_id,omitempty"`
		Email        string `yaml:"email,omitempty"`
		Password     string `yaml:"password,omitempty"`
		RefreshToken string `yaml:"refresh_token,omitempty"`
	} `yaml:"firebase"`
	DBPath string `yaml:"db_path,omitempty"` // optional, defaults to ./data/opencode-manager.db
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			runLogin()
			return
		case "relogin":
			runRelogin()
			return
		case "setup":
			runSetup()
			return
		}
	}

	runServe()
}

// Default Firebase project values (from environment.ts).
const (
	defaultAPIKey  = "AIzaSyCECBGZeLmLdi2a8Viii7iIoYksLKlDPPY"
	defaultDBURL   = "https://opencode-manager-default-rtdb.firebaseio.com"
	defaultAuthDom = "opencode-manager.firebaseapp.com"
	defaultProjID  = "opencode-manager"
)

type loginResult struct {
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"`
	UID          string `json:"uid"`
}

type firebaseProjectConfig struct {
	APIKey      string
	DatabaseURL string
	AuthDomain  string
	ProjectID   string
}

const totalLoginSteps = 4

func runLogin() {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	credPath := fs.String("credentials", "./credentials.yaml", "path to Firebase credentials file")
	apiKeyFlag := fs.String("api-key", "", "Firebase web API key")
	databaseURLFlag := fs.String("database-url", "", "Firebase Realtime Database URL")
	authDomainFlag := fs.String("auth-domain", "", "Firebase auth domain")
	projectIDFlag := fs.String("project-id", "", "Firebase project ID")
	_ = fs.Parse(os.Args[2:])

	reader := bufio.NewReader(os.Stdin)
	projectCfg, err := resolveFirebaseProjectConfig(*credPath, *apiKeyFlag, *databaseURLFlag, *authDomainFlag, *projectIDFlag)
	if err != nil {
		printFail("Invalid Firebase project configuration: %v", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("  \033[1m\033[36m┌──────────────────────────────────────┐\033[0m")
	fmt.Println("  \033[1m\033[36m│     OpenCode Manager Setup           │\033[0m")
	fmt.Println("  \033[1m\033[36m└──────────────────────────────────────┘\033[0m")
	fmt.Println()

	if _, err := os.Stat(*credPath); err == nil {
		fmt.Printf("  \033[33m! %s already exists. Overwrite? [y/N]: \033[0m", *credPath)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("  Aborted.")
			return
		}
		fmt.Println()
	}

	// ── Step 1: Browser login ──────────────────────────────────────────
	printLoginStep(1, "Sign in to Firebase")
	fmt.Println("  \033[33mA browser window will open. Sign in with Google or email.\033[0m")
	fmt.Println()

	refreshToken, email := doBrowserLogin(projectCfg)
	printOK("Signed in as %s", email)
	fmt.Println()

	// Connect to Firebase for subsequent steps.
	fbAuth := firebase.NewAuth(projectCfg.APIKey)
	if err := fbAuth.SignInWithRefreshToken(refreshToken); err != nil {
		printFail("Token verification failed: %v", err)
		os.Exit(1)
	}
	rtdb := firebase.NewRTDB(projectCfg.DatabaseURL, fbAuth)
	ctx := context.Background()

	// Check for existing config.
	remoteConfig := firebase.NewRemoteConfig(rtdb)
	existing, _ := remoteConfig.Pull(ctx)

	// ── Step 2: Telegram Bot ───────────────────────────────────────────
	printLoginStep(2, "Telegram Bot")
	fmt.Println("  \033[33mCreate a bot via @BotFather on Telegram to get your token.\033[0m")
	fmt.Println()

	defaultToken := existing["telegram.token"]
	tokenDisplay := ""
	if defaultToken != "" {
		tokenDisplay = maskToken(defaultToken)
	}
	token := promptWithDefault(reader, "Bot token", defaultToken, tokenDisplay)
	if token == "" {
		token = defaultToken
	}

	defaultUsers := existing["telegram.allowed_users"]
	fmt.Println("  \033[33mSend /start to @userinfobot to find your Telegram user ID.\033[0m")
	users := promptWithDefault(reader, "Allowed user IDs (comma-separated)", defaultUsers, "")
	if users == "" {
		users = defaultUsers
	}
	printOK("Telegram configured")
	fmt.Println()

	// ── Step 3: Binary paths ───────────────────────────────────────────
	printLoginStep(3, "AI Coding Tools")

	claudeBin := detectBinary("claude", "Claude Code")
	defaultClaude := existing["process.claudecode_binary"]
	if defaultClaude == "" {
		defaultClaude = claudeBin
	}
	claudePath := promptWithDefault(reader, "Claude Code binary", defaultClaude, "")
	if claudePath == "" {
		claudePath = defaultClaude
	}

	opencodeBin := detectBinary("opencode", "OpenCode")
	defaultOpencode := existing["process.opencode_binary"]
	if defaultOpencode == "" {
		defaultOpencode = opencodeBin
	}
	opencodePath := promptWithDefault(reader, "OpenCode binary", defaultOpencode, "")
	if opencodePath == "" {
		opencodePath = defaultOpencode
	}

	printOK("Tools configured")
	fmt.Println()

	// ── Step 4: Save everything ────────────────────────────────────────
	printLoginStep(4, "Save configuration")

	// Save credentials locally.
	content := fmt.Sprintf(`firebase:
  api_key: %q
  database_url: %q
  auth_domain: %q
  project_id: %q
  refresh_token: %q
`, projectCfg.APIKey, projectCfg.DatabaseURL, projectCfg.AuthDomain, projectCfg.ProjectID, refreshToken)

	if err := os.WriteFile(*credPath, []byte(content), 0600); err != nil {
		printFail("Failed to write %s: %v", *credPath, err)
		os.Exit(1)
	}
	printOK("Credentials saved to %s", *credPath)

	// Push config to Firebase.
	settings := map[string]string{
		"telegram.token":            token,
		"telegram.allowed_users":    users,
		"process.claudecode_binary": claudePath,
		"process.opencode_binary":   opencodePath,
	}
	// Preserve existing settings not covered by this wizard.
	for k, v := range existing {
		if _, overridden := settings[k]; !overridden {
			settings[k] = v
		}
	}

	if err := remoteConfig.Push(ctx, settings); err != nil {
		printFail("Failed to push config to Firebase: %v", err)
		os.Exit(1)
	}
	printOK("Config pushed to Firebase (%d keys)", len(settings))
	fmt.Println()

	// ── Done ───────────────────────────────────────────────────────────
	fmt.Println("  \033[1m\033[32m┌──────────────────────────────────────┐\033[0m")
	fmt.Println("  \033[1m\033[32m│           Setup Complete!            │\033[0m")
	fmt.Println("  \033[1m\033[32m└──────────────────────────────────────┘\033[0m")
	fmt.Println()
	fmt.Printf("  Run: \033[36m%s\033[0m\n", os.Args[0])
	fmt.Println()
}

func runRelogin() {
	fs := flag.NewFlagSet("relogin", flag.ExitOnError)
	credPath := fs.String("credentials", "./credentials.yaml", "path to Firebase credentials file")
	_ = fs.Parse(os.Args[2:])

	creds, err := readCredentials(*credPath)
	if err != nil {
		printFail("Failed to read %s: %v", *credPath, err)
		os.Exit(1)
	}

	if _, err := reloginCredentials(*credPath, creds); err != nil {
		printFail("Re-login failed: %v", err)
		os.Exit(1)
	}
}

func doBrowserLogin(projectCfg firebaseProjectConfig) (refreshToken, email string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		printFail("Failed to start local server: %v", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	resultCh := make(chan loginResult, 1)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page := loginpage.HTML
		page = strings.ReplaceAll(page, "{{API_KEY}}", projectCfg.APIKey)
		page = strings.ReplaceAll(page, "{{AUTH_DOMAIN}}", projectCfg.AuthDomain)
		page = strings.ReplaceAll(page, "{{DATABASE_URL}}", projectCfg.DatabaseURL)
		page = strings.ReplaceAll(page, "{{PROJECT_ID}}", projectCfg.ProjectID)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	})

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		var result loginResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"ok":true}`)
		resultCh <- result
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("login server error", "error", err)
		}
	}()

	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Printf("  Opening browser at \033[36m%s\033[0m ...\n", url)
	openBrowser(url)

	fmt.Print("  Waiting for sign-in... ")
	result := <-resultCh
	_ = server.Close()

	return result.RefreshToken, result.Email
}

func reloginCredentials(credPath string, creds *credentialsFile) (*credentialsFile, error) {
	projectCfg, err := projectConfigFromCredentials(creds)
	if err != nil {
		return nil, err
	}

	fmt.Println()
	fmt.Println("  \033[1m\033[36m┌──────────────────────────────────────┐\033[0m")
	fmt.Println("  \033[1m\033[36m│    Refresh Firebase Credentials      │\033[0m")
	fmt.Println("  \033[1m\033[36m└──────────────────────────────────────┘\033[0m")
	fmt.Println()
	fmt.Println("  \033[33mA browser window will open. Sign in again to refresh the stored token.\033[0m")
	fmt.Println()

	refreshToken, email := doBrowserLogin(projectCfg)
	printOK("Signed in as %s", email)

	updated := *creds
	updated.Firebase.APIKey = projectCfg.APIKey
	updated.Firebase.DatabaseURL = projectCfg.DatabaseURL
	updated.Firebase.AuthDomain = projectCfg.AuthDomain
	updated.Firebase.ProjectID = projectCfg.ProjectID
	updated.Firebase.RefreshToken = refreshToken

	if err := writeCredentials(credPath, &updated); err != nil {
		return nil, err
	}

	printOK("Credentials refreshed in %s", credPath)
	fmt.Println()
	return &updated, nil
}

func resolveFirebaseProjectConfig(credPath, apiKey, databaseURL, authDomain, projectID string) (firebaseProjectConfig, error) {
	cfg := firebaseProjectConfig{
		APIKey:      defaultAPIKey,
		DatabaseURL: defaultDBURL,
		AuthDomain:  defaultAuthDom,
		ProjectID:   defaultProjID,
	}

	if creds, err := readCredentials(credPath); err == nil {
		if creds.Firebase.APIKey != "" {
			cfg.APIKey = creds.Firebase.APIKey
		}
		if creds.Firebase.DatabaseURL != "" {
			cfg.DatabaseURL = creds.Firebase.DatabaseURL
		}
		if creds.Firebase.AuthDomain != "" {
			cfg.AuthDomain = creds.Firebase.AuthDomain
		}
		if creds.Firebase.ProjectID != "" {
			cfg.ProjectID = creds.Firebase.ProjectID
		}
	}

	if apiKey != "" {
		cfg.APIKey = apiKey
	}
	if databaseURL != "" {
		cfg.DatabaseURL = databaseURL
	}
	if projectID != "" {
		cfg.ProjectID = projectID
	}
	if authDomain != "" {
		cfg.AuthDomain = authDomain
	}

	if cfg.ProjectID == "" {
		cfg.ProjectID = deriveProjectID(cfg.DatabaseURL)
	}
	if cfg.AuthDomain == "" && cfg.ProjectID != "" {
		cfg.AuthDomain = cfg.ProjectID + ".firebaseapp.com"
	}
	if cfg.APIKey == "" || cfg.DatabaseURL == "" || cfg.AuthDomain == "" || cfg.ProjectID == "" {
		return firebaseProjectConfig{}, fmt.Errorf("api_key, database_url, auth_domain, and project_id are required for login")
	}
	return cfg, nil
}

func projectConfigFromCredentials(creds *credentialsFile) (firebaseProjectConfig, error) {
	cfg := firebaseProjectConfig{
		APIKey:      creds.Firebase.APIKey,
		DatabaseURL: creds.Firebase.DatabaseURL,
		AuthDomain:  creds.Firebase.AuthDomain,
		ProjectID:   creds.Firebase.ProjectID,
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = deriveProjectID(cfg.DatabaseURL)
	}
	if cfg.AuthDomain == "" && cfg.ProjectID != "" {
		cfg.AuthDomain = cfg.ProjectID + ".firebaseapp.com"
	}
	if cfg.APIKey == "" || cfg.DatabaseURL == "" || cfg.AuthDomain == "" || cfg.ProjectID == "" {
		return firebaseProjectConfig{}, fmt.Errorf("credentials.yaml must include api_key, database_url, and enough project metadata to derive auth_domain/project_id")
	}
	return cfg, nil
}

func deriveProjectID(databaseURL string) string {
	dbURL := strings.TrimPrefix(databaseURL, "https://")
	dbURL = strings.TrimPrefix(dbURL, "http://")
	host := dbURL
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	switch {
	case strings.HasSuffix(host, "-default-rtdb.firebaseio.com"):
		return strings.TrimSuffix(host, "-default-rtdb.firebaseio.com")
	case strings.HasSuffix(host, ".firebaseio.com"):
		return strings.TrimSuffix(host, ".firebaseio.com")
	case strings.HasSuffix(host, ".firebasedatabase.app"):
		return strings.TrimSuffix(host, ".firebasedatabase.app")
	default:
		return ""
	}
}

func detectBinary(name, label string) string {
	if p, err := exec.LookPath(name); err == nil {
		printOK("Detected %s: %s", label, p)
		return p
	}
	return name // fallback to just the name
}

func promptWithDefault(r *bufio.Reader, prompt, defaultVal, displayOverride string) string {
	display := defaultVal
	if displayOverride != "" {
		display = displayOverride
	}
	if display != "" {
		fmt.Printf("  %s [\033[36m%s\033[0m]: ", prompt, display)
	} else {
		fmt.Printf("  %s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func maskToken(token string) string {
	if len(token) < 10 {
		return "****"
	}
	return token[:6] + "..." + token[len(token)-4:]
}

func printLoginStep(n int, title string) {
	fmt.Printf("  \033[1mStep %d/%d: %s\033[0m\n", n, totalLoginSteps, title)
}

func printOK(format string, args ...any) {
	fmt.Printf("  \033[32m✓ %s\033[0m\n", fmt.Sprintf(format, args...))
}

func printFail(format string, args ...any) {
	fmt.Printf("  \033[31m✗ %s\033[0m\n", fmt.Sprintf(format, args...))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
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

func writeCredentials(path string, creds *credentialsFile) error {
	data, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func openStore(dbPath string) (*store.SQLiteStore, error) {
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
		fmt.Fprintf(os.Stderr, "  login    Browser login + interactive cloud setup\n")
		fmt.Fprintf(os.Stderr, "  relogin  Refresh Firebase browser credentials in credentials.yaml\n")
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
			fbClient, err = newFirebaseClient(creds)
		}
	}
	if err != nil {
		slog.Error("firebase connection failed", "error", err)
		os.Exit(1)
	}

	// Pull config from Firebase.
	remoteConfig := firebase.NewRemoteConfig(fbClient.RTDB)
	settings, err := remoteConfig.Pull(ctx)
	if err != nil {
		if nextCreds, recovered := maybeRecoverFirebaseCredentials(*credPath, creds, err); recovered {
			creds = nextCreds
			fbClient, err = newFirebaseClient(creds)
			if err == nil {
				remoteConfig = firebase.NewRemoteConfig(fbClient.RTDB)
				settings, err = remoteConfig.Pull(ctx)
			}
		}
	}
	if err != nil {
		slog.Error("failed to pull config from Firebase", "error", err)
		os.Exit(1)
	}

	if len(settings) == 0 {
		slog.Info("no config found in Firebase — waiting for web frontend setup...")
		slog.Info("open the web frontend and configure Telegram token, allowed users, etc.")
		settings, err = remoteConfig.WaitForConfig(ctx)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("shutting down")
				os.Exit(0)
			}
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

	// Create store: Firestore (cloud) or SQLite (fallback).
	var st store.Store
	if fbClient.Firestore != nil {
		st = store.NewFirestoreStore(ctx, newFirestoreAdapter(fbClient))
		slog.Info("using Firestore for persistent storage")
	} else {
		dp := getDBPath(creds, *dbPathFlag)
		sqlSt, err := openStore(dp)
		if err != nil {
			slog.Error("failed to open local database", "error", err)
			os.Exit(1)
		}
		st = sqlSt
		if err := st.SetSettings(settings); err != nil {
			slog.Warn("failed to cache settings locally", "error", err)
		}
		slog.Info("using SQLite for persistent storage (no ProjectID)", "db", dp)
	}

	// Create and start application.
	application, err := app.New(cfg, st, *devMode)
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

// newFirestoreAdapter bridges firebase.Firestore → store.FirestoreClient,
// converting firebase.Document → store.FirestoreDoc.
func newFirestoreAdapter(fbClient *firebase.Client) store.FirestoreClient {
	fs := fbClient.Firestore
	return &store.FirestoreAdapter{
		SetDocFn:    fs.SetDoc,
		UpdateDocFn: fs.UpdateDoc,
		DeleteDocFn: fs.DeleteDoc,
		GetDocFn: func(ctx context.Context, path string) (*store.FirestoreDoc, error) {
			doc, err := fs.GetDoc(ctx, path)
			if err != nil || doc == nil {
				return nil, err
			}
			return &store.FirestoreDoc{
				Name:       doc.Name,
				Fields:     doc.Fields,
				CreateTime: doc.CreateTime,
				UpdateTime: doc.UpdateTime,
			}, nil
		},
		ListDocsFn: func(ctx context.Context, collectionPath string) ([]*store.FirestoreDoc, error) {
			docs, err := fs.ListDocs(ctx, collectionPath)
			if err != nil {
				return nil, err
			}
			result := make([]*store.FirestoreDoc, len(docs))
			for i, doc := range docs {
				result[i] = &store.FirestoreDoc{
					Name:       doc.Name,
					Fields:     doc.Fields,
					CreateTime: doc.CreateTime,
					UpdateTime: doc.UpdateTime,
				}
			}
			return result, nil
		},
	}
}

func newFirebaseClient(creds *credentialsFile) (*firebase.Client, error) {
	projectID := creds.Firebase.ProjectID
	if projectID == "" {
		projectID = deriveProjectID(creds.Firebase.DatabaseURL)
	}
	return firebase.NewClient(firebase.Config{
		APIKey:       creds.Firebase.APIKey,
		DatabaseURL:  creds.Firebase.DatabaseURL,
		ProjectID:    projectID,
		Email:        creds.Firebase.Email,
		Password:     creds.Firebase.Password,
		RefreshToken: creds.Firebase.RefreshToken,
	})
}

func maybeRecoverFirebaseCredentials(credPath string, creds *credentialsFile, cause error) (*credentialsFile, bool) {
	if !shouldOfferRelogin(creds, cause) {
		return creds, false
	}

	cmdName := filepath.Base(os.Args[0])
	slog.Warn("firebase credentials may need re-login",
		"error", cause,
		"hint", fmt.Sprintf("run `%s relogin --credentials %s` to refresh browser credentials", cmdName, credPath))

	if !isInteractiveTerminal() {
		return creds, false
	}

	fmt.Fprintf(os.Stderr, "\nFirebase 凭证可能已失效或无权限，是否现在重新登录并更新 %s? [y/N]: ", credPath)
	reader := bufio.NewReader(os.Stdin)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(strings.ToLower(ans))
	if ans != "y" && ans != "yes" {
		return creds, false
	}

	updated, err := reloginCredentials(credPath, creds)
	if err != nil {
		slog.Error("firebase re-login failed", "error", err)
		return creds, false
	}

	slog.Info("firebase credentials refreshed; retrying")
	return updated, true
}

func shouldOfferRelogin(creds *credentialsFile, err error) bool {
	if creds == nil || creds.Firebase.RefreshToken == "" || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refresh token invalid") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "auth_revoked")
}

func isInteractiveTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
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
