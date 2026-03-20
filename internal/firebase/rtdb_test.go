package firebase

import (
	"context"
	"testing"
	"time"
)

func TestRTDBNewRequestUsesAuthQueryParam(t *testing.T) {
	auth := &Auth{
		idToken:   "test-id-token",
		expiresAt: time.Now().Add(time.Minute),
	}
	rtdb := NewRTDB("https://example-default-rtdb.firebaseio.com", auth)

	req, err := rtdb.newRequest(context.Background(), "GET", "config", nil)
	if err != nil {
		t.Fatalf("newRequest returned error: %v", err)
	}

	if got := req.URL.String(); got != "https://example-default-rtdb.firebaseio.com/config.json?auth=test-id-token" {
		t.Fatalf("unexpected request URL: %s", got)
	}

	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("expected no Authorization header, got %q", got)
	}
}
