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
	"strings"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/firebase/loginpage"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type loginResult struct {
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"`
	UID          string `json:"uid"`
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
	loginClientID := uuid.New().String()
	fbClient, err := firebase.NewClient(firebase.Config{
		APIKey:       projectCfg.APIKey,
		DatabaseURL:  projectCfg.DatabaseURL,
		ProjectID:    projectCfg.ProjectID,
		RefreshToken: refreshToken,
		ClientID:     loginClientID,
	})
	if err != nil {
		printFail("Firebase connection failed: %v", err)
		os.Exit(1)
	}
	ctx := context.Background()

	// Create store to read/write config.
	loginStore := store.NewFirestoreStore(ctx, newFirestoreAdapter(fbClient), fbClient.UID())

	// Check for existing config.
	existing, _ := loginStore.GetUserConfig()
	if existing == nil {
		existing = make(map[string]string)
	}
	existingClient, _ := loginStore.GetClientConfig(loginClientID)
	if existingClient == nil {
		existingClient = make(map[string]string)
	}

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
	defaultClaude := existingClient["process.claudecode_binary"]
	if defaultClaude == "" {
		defaultClaude = claudeBin
	}
	claudePath := promptWithDefault(reader, "Claude Code binary", defaultClaude, "")
	if claudePath == "" {
		claudePath = defaultClaude
	}

	opencodeBin := detectBinary("opencode", "OpenCode")
	defaultOpencode := existingClient["process.opencode_binary"]
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

	// Save credentials locally (with auto-generated client_id).
	credClientID := uuid.New().String()
	content := fmt.Sprintf(`firebase:
  api_key: %q
  database_url: %q
  auth_domain: %q
  project_id: %q
  refresh_token: %q
client_id: %q
`, projectCfg.APIKey, projectCfg.DatabaseURL, projectCfg.AuthDomain, projectCfg.ProjectID, refreshToken, credClientID)

	if err := os.WriteFile(*credPath, []byte(content), 0600); err != nil {
		printFail("Failed to write %s: %v", *credPath, err)
		os.Exit(1)
	}
	printOK("Credentials saved to %s", *credPath)

	// Push user config to Firestore.
	userSettings := map[string]string{
		"telegram.token":         token,
		"telegram.allowed_users": users,
	}
	for k, v := range existing {
		if _, overridden := userSettings[k]; !overridden {
			userSettings[k] = v
		}
	}

	if err := loginStore.SetUserConfig(userSettings); err != nil {
		printFail("Failed to push user config to Firestore: %v", err)
		os.Exit(1)
	}
	printOK("User config pushed to Firestore (%d keys)", len(userSettings))

	// Push client config to Firestore.
	clientSettings := map[string]string{
		"process.claudecode_binary": claudePath,
		"process.opencode_binary":   opencodePath,
	}
	for k, v := range existingClient {
		if _, overridden := clientSettings[k]; !overridden {
			clientSettings[k] = v
		}
	}

	if err := loginStore.SetClientConfig(credClientID, clientSettings); err != nil {
		printFail("Failed to push client config to Firestore: %v", err)
		os.Exit(1)
	}
	printOK("Client config pushed to Firestore (%d keys)", len(clientSettings))
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
