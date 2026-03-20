package firebase

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	urlpkg "net/url"
	"sync"
	"time"
)

// Auth handles Firebase email/password authentication via REST API.
// Go signs in as a regular user — no Admin SDK or service account needed.
type Auth struct {
	apiKey string

	mu           sync.RWMutex
	idToken      string
	refreshToken string
	expiresAt    time.Time
	uid          string
}

func NewAuth(apiKey string) *Auth {
	return &Auth{apiKey: apiKey}
}

// SignIn authenticates with email/password and stores the ID token.
func (a *Auth) SignIn(email, password string) error {
	url := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=" + a.apiKey

	body, _ := json.Marshal(map[string]interface{}{
		"email":             email,
		"password":          password,
		"returnSecureToken": true,
	})

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("firebase sign in request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if decErr := json.NewDecoder(resp.Body).Decode(&errResp); decErr != nil {
			return fmt.Errorf("firebase sign in failed: status %d", resp.StatusCode)
		}
		return fmt.Errorf("firebase sign in failed: %s", errResp.Error.Message)
	}

	var result struct {
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding sign in response: %w", err)
	}

	a.mu.Lock()
	a.idToken = result.IDToken
	a.refreshToken = result.RefreshToken
	a.expiresAt = time.Now().Add(55 * time.Minute) // Firebase tokens expire in 3600s
	a.uid = extractUID(result.IDToken)
	a.mu.Unlock()

	slog.Info("firebase: signed in successfully", "uid", a.uid)
	return nil
}

// SignInWithRefreshToken authenticates using a stored refresh token (from browser login).
func (a *Auth) SignInWithRefreshToken(rt string) error {
	a.mu.Lock()
	a.refreshToken = rt
	a.expiresAt = time.Time{} // Force immediate refresh
	a.mu.Unlock()

	// Verify by getting a token.
	if _, err := a.Token(); err != nil {
		return fmt.Errorf("firebase refresh token invalid: %w", err)
	}
	slog.Info("firebase: signed in with refresh token")
	return nil
}

// Token returns a valid ID token, refreshing if needed.
func (a *Auth) Token() (string, error) {
	a.mu.RLock()
	if time.Now().Before(a.expiresAt) {
		token := a.idToken
		a.mu.RUnlock()
		return token, nil
	}
	a.mu.RUnlock()

	return a.refresh()
}

func (a *Auth) refresh() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Double-check after acquiring write lock.
	if time.Now().Before(a.expiresAt) {
		return a.idToken, nil
	}

	url := "https://securetoken.googleapis.com/v1/token?key=" + a.apiKey

	form := urlpkg.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", a.refreshToken)

	resp, err := http.Post(url, "application/x-www-form-urlencoded", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("firebase token refresh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("firebase token refresh failed: status %d", resp.StatusCode)
	}

	var result struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    string `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding refresh response: %w", err)
	}

	a.idToken = result.IDToken
	a.refreshToken = result.RefreshToken
	a.expiresAt = time.Now().Add(55 * time.Minute)
	a.uid = extractUID(result.IDToken)

	slog.Debug("firebase: token refreshed", "uid", a.uid)
	return a.idToken, nil
}

// UID returns the Firebase user ID extracted from the current ID token.
func (a *Auth) UID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.uid
}

// extractUID decodes the JWT payload (middle segment) and extracts user_id.
func extractUID(idToken string) string {
	parts := strings.SplitN(idToken, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	// JWT payload is base64url-encoded; add padding if needed.
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		slog.Warn("firebase: failed to decode JWT payload", "error", err)
		return ""
	}
	var claims struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		slog.Warn("firebase: failed to parse JWT claims", "error", err)
		return ""
	}
	return claims.UserID
}
