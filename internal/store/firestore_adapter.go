package store

import (
	"context"
	"strings"
)

// FirestoreAdapter implements FirestoreClient by delegating to closures.
// Created in the wiring layer (main.go) to bridge firebase.Firestore → store.FirestoreClient
// without import cycles.
type FirestoreAdapter struct {
	GetDocFn    func(ctx context.Context, path string) (*FirestoreDoc, error)
	SetDocFn    func(ctx context.Context, path string, fields map[string]interface{}) error
	UpdateDocFn func(ctx context.Context, path string, fields map[string]interface{}) error
	DeleteDocFn func(ctx context.Context, path string) error
	ListDocsFn  func(ctx context.Context, collectionPath string) ([]*FirestoreDoc, error)
}

func (a *FirestoreAdapter) GetDoc(ctx context.Context, path string) (*FirestoreDoc, error) {
	return a.GetDocFn(ctx, path)
}

func (a *FirestoreAdapter) SetDoc(ctx context.Context, path string, fields map[string]interface{}) error {
	return a.SetDocFn(ctx, path, fields)
}

func (a *FirestoreAdapter) UpdateDoc(ctx context.Context, path string, fields map[string]interface{}) error {
	return a.UpdateDocFn(ctx, path, fields)
}

func (a *FirestoreAdapter) DeleteDoc(ctx context.Context, path string) error {
	return a.DeleteDocFn(ctx, path)
}

func (a *FirestoreAdapter) ListDocs(ctx context.Context, collectionPath string) ([]*FirestoreDoc, error) {
	return a.ListDocsFn(ctx, collectionPath)
}

// DocIDFromName extracts the document ID from a Firestore resource name.
// e.g., "projects/x/databases/(default)/documents/instances/abc123" → "abc123"
func DocIDFromName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
