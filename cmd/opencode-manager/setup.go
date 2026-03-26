package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type firebaseProjectConfig struct {
	APIKey      string
	DatabaseURL string
	AuthDomain  string
	ProjectID   string
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
	serverKey := creds.Firebase.ServerAPIKey
	if serverKey == "" {
		serverKey = defaultServerAPIKey
	}
	return firebase.NewClient(firebase.Config{
		APIKey:       creds.Firebase.APIKey,
		ServerAPIKey: serverKey,
		DatabaseURL:  creds.Firebase.DatabaseURL,
		ProjectID:    projectID,
		Email:        creds.Firebase.Email,
		Password:     creds.Firebase.Password,
		RefreshToken: creds.Firebase.RefreshToken,
		ClientID:     creds.ClientID,
	})
}

// ensureClientID auto-generates a client_id if not present and persists it.
func ensureClientID(creds *credentialsFile, credPath string) {
	if creds.ClientID != "" {
		return
	}
	creds.ClientID = uuid.New().String()
	if err := writeCredentials(credPath, creds); err != nil {
		slog.Warn("failed to persist auto-generated client_id", "error", err)
	} else {
		slog.Info("auto-generated client_id", "client_id", creds.ClientID)
	}
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

// migrateFromRTDB reads legacy config from RTDB /config and migrates it
// to Firestore user/client config docs. Returns the migrated configs.
func migrateFromRTDB(ctx context.Context, fbClient *firebase.Client, st store.Store, clientID string) (userConfig, clientConfig map[string]string) {
	var raw map[string]interface{}
	if err := fbClient.RTDB.Get(ctx, "config", &raw); err != nil || len(raw) == 0 {
		return nil, nil
	}

	slog.Info("found legacy config in RTDB /config, migrating to Firestore...", "keys", len(raw))

	// Convert to flat string map.
	legacy := make(map[string]string, len(raw))
	for k, v := range raw {
		legacy[k] = fmt.Sprint(v)
	}

	// Split into user-level and client-level settings.
	userConfig = make(map[string]string)
	clientConfig = make(map[string]string)

	userKeys := map[string]bool{
		"telegram.token": true, "telegram.allowed_users": true,
		"telegram.board_interval": true, "web.enabled": true, "web.addr": true,
	}
	clientKeys := map[string]bool{
		"process.opencode_binary": true, "process.claudecode_binary": true,
		"process.port_range_start": true, "process.port_range_end": true,
		"process.health_check_interval": true, "process.max_restart_attempts": true,
	}

	for k, v := range legacy {
		if userKeys[k] {
			userConfig[k] = v
		} else if clientKeys[k] {
			clientConfig[k] = v
		}
	}

	// Write to Firestore.
	if len(userConfig) > 0 {
		if err := st.SetUserConfig(userConfig); err != nil {
			slog.Error("failed to migrate user config to Firestore", "error", err)
			return nil, nil
		}
	}
	if len(clientConfig) > 0 {
		if err := st.SetClientConfig(clientID, clientConfig); err != nil {
			slog.Error("failed to migrate client config to Firestore", "error", err)
			return userConfig, nil
		}
	}

	slog.Info("config migrated from RTDB to Firestore",
		"user_keys", len(userConfig), "client_keys", len(clientConfig))

	return userConfig, clientConfig
}
