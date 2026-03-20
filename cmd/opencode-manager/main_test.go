package main

import (
	"errors"
	"testing"
)

func TestDeriveProjectID(t *testing.T) {
	tests := map[string]string{
		"https://example-default-rtdb.firebaseio.com": "example",
		"https://example.firebaseio.com":              "example",
		"https://example.firebasedatabase.app":        "example",
		"https://invalid.example.com":                 "",
	}

	for databaseURL, want := range tests {
		if got := deriveProjectID(databaseURL); got != want {
			t.Fatalf("deriveProjectID(%q) = %q, want %q", databaseURL, got, want)
		}
	}
}

func TestProjectConfigFromCredentialsDerivesMissingFields(t *testing.T) {
	var creds credentialsFile
	creds.Firebase.APIKey = "api-key"
	creds.Firebase.DatabaseURL = "https://example-default-rtdb.firebaseio.com"

	cfg, err := projectConfigFromCredentials(&creds)
	if err != nil {
		t.Fatalf("projectConfigFromCredentials returned error: %v", err)
	}

	if cfg.ProjectID != "example" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "example")
	}
	if cfg.AuthDomain != "example.firebaseapp.com" {
		t.Fatalf("AuthDomain = %q, want %q", cfg.AuthDomain, "example.firebaseapp.com")
	}
}

func TestShouldOfferRelogin(t *testing.T) {
	var creds credentialsFile
	creds.Firebase.RefreshToken = "refresh-token"

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "permission denied", err: errors.New("RTDB get config: status 401: Permission denied"), want: true},
		{name: "refresh token invalid", err: errors.New("firebase refresh token invalid"), want: true},
		{name: "non-auth error", err: errors.New("dial tcp timeout"), want: false},
	}

	for _, tc := range cases {
		if got := shouldOfferRelogin(&creds, tc.err); got != tc.want {
			t.Fatalf("%s: shouldOfferRelogin() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
