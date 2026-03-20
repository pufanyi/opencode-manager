package firebase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	a.mu.Unlock()

	slog.Info("firebase: signed in successfully")
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

	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": a.refreshToken,
	})

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding refresh response: %w", err)
	}

	a.idToken = result.IDToken
	a.refreshToken = result.RefreshToken
	a.expiresAt = time.Now().Add(55 * time.Minute)

	slog.Debug("firebase: token refreshed")
	return a.idToken, nil
}
