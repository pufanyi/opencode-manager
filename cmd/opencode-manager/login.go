package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/firebase/loginpage"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type loginResult struct {
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"`
	UID          string `json:"uid"`
}

// doFirstTimeSetup runs an inline browser login and saves credentials.yaml.
// Called when credentials.yaml does not exist.
func doFirstTimeSetup(credPath string) (*credentialsFile, error) {
	projectCfg := firebaseProjectConfig{
		APIKey:      defaultAPIKey,
		DatabaseURL: defaultDBURL,
		AuthDomain:  defaultAuthDom,
		ProjectID:   defaultProjID,
	}

	fmt.Println()
	fmt.Println("  \033[1m\033[36m┌──────────────────────────────────────┐\033[0m")
	fmt.Println("  \033[1m\033[36m│     OpenCode Manager Setup           │\033[0m")
	fmt.Println("  \033[1m\033[36m└──────────────────────────────────────┘\033[0m")
	fmt.Println()
	fmt.Println("  \033[1mStep 1: Sign in to Firebase\033[0m")
	fmt.Println("  \033[33mA browser window will open. Sign in with Google or email.\033[0m")
	fmt.Println()

	refreshToken, email := doBrowserLogin(projectCfg)
	printOK("Signed in as %s", email)
	fmt.Println()

	creds := &credentialsFile{}
	creds.Firebase.APIKey = projectCfg.APIKey
	creds.Firebase.ServerAPIKey = defaultServerAPIKey
	creds.Firebase.DatabaseURL = projectCfg.DatabaseURL
	creds.Firebase.AuthDomain = projectCfg.AuthDomain
	creds.Firebase.ProjectID = projectCfg.ProjectID
	creds.Firebase.RefreshToken = refreshToken
	creds.ClientID = uuid.New().String()

	if err := writeCredentials(credPath, creds); err != nil {
		return nil, fmt.Errorf("saving credentials: %w", err)
	}
	printOK("Credentials saved to %s", credPath)
	fmt.Println()

	return creds, nil
}

// doConfigSetup prompts the user for Telegram and binary config, saves to Firestore.
// Called when no config exists in Firestore.
func doConfigSetup(st store.Store, clientID string) (userConfig, clientConfig map[string]string) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("  \033[1mConfigure Telegram Bot\033[0m")
	fmt.Println("  \033[33mCreate a bot via @BotFather on Telegram to get your token.\033[0m")
	fmt.Println()

	token := promptWithDefault(reader, "Bot token", "", "")
	if token == "" {
		printFail("Telegram bot token is required")
		os.Exit(1)
	}

	fmt.Println("  \033[33mSend /start to @userinfobot to find your Telegram user ID.\033[0m")
	users := promptWithDefault(reader, "Allowed user IDs (comma-separated)", "", "")
	if users == "" {
		printFail("At least one allowed user ID is required")
		os.Exit(1)
	}
	printOK("Telegram configured")
	fmt.Println()

	fmt.Println("  \033[1mConfigure AI Coding Tools\033[0m")

	claudeBin := detectBinary("claude", "Claude Code")
	claudePath := promptWithDefault(reader, "Claude Code binary", claudeBin, "")
	if claudePath == "" {
		claudePath = claudeBin
	}

	opencodeBin := detectBinary("opencode", "OpenCode")
	opencodePath := promptWithDefault(reader, "OpenCode binary", opencodeBin, "")
	if opencodePath == "" {
		opencodePath = opencodeBin
	}

	printOK("Tools configured")
	fmt.Println()

	userConfig = map[string]string{
		"telegram.token":         token,
		"telegram.allowed_users": users,
	}
	clientConfig = map[string]string{
		"process.claudecode_binary": claudePath,
		"process.opencode_binary":   opencodePath,
	}

	if err := st.SetUserConfig(userConfig); err != nil {
		printFail("Failed to save user config: %v", err)
		os.Exit(1)
	}
	printOK("User config saved to Firestore")

	if err := st.SetClientConfig(clientID, clientConfig); err != nil {
		printFail("Failed to save client config: %v", err)
		os.Exit(1)
	}
	printOK("Client config saved to Firestore")
	fmt.Println()

	return userConfig, clientConfig
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
