package firebase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Firestore is a client for the Firestore REST API.
// Uses the same user-level auth as RTDB (not Admin SDK).
type Firestore struct {
	projectID string
	auth      *Auth
	baseURL   string
}

func NewFirestore(projectID string, auth *Auth) *Firestore {
	return &Firestore{
		projectID: projectID,
		auth:      auth,
		baseURL:   fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents", projectID),
	}
}

// Document represents a Firestore document with decoded fields.
type Document struct {
	Name       string                 // Full resource path
	Fields     map[string]interface{} // Decoded Go values
	CreateTime string
	UpdateTime string
}

// docID extracts the document ID from the full resource name.
func (d *Document) DocID() string {
	parts := strings.Split(d.Name, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// ── CRUD Operations ─────────────────────────────────────────────────────────

// GetDoc reads a single document. Returns nil, nil if not found.
func (f *Firestore) GetDoc(ctx context.Context, path string) (*Document, error) {
	req, err := f.newRequest(ctx, "GET", f.docURL(path), nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firestore get %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, f.responseError("get", path, resp)
	}

	return f.decodeDocument(resp.Body)
}

// SetDoc creates or overwrites a document at the given path.
// Path format: "collection/docID" or "collection/docID/subcollection/subDocID".
func (f *Firestore) SetDoc(ctx context.Context, path string, fields map[string]interface{}) error {
	collection, docID := splitDocPath(path)

	encoded := encodeFields(fields)
	body, err := json.Marshal(map[string]interface{}{"fields": encoded})
	if err != nil {
		return fmt.Errorf("marshaling document: %w", err)
	}

	// Use PATCH to create-or-replace (upsert).
	reqURL := f.docURL(collection + "/" + docID)
	req, err := f.newRequest(ctx, "PATCH", reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("firestore set %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return f.responseError("set", path, resp)
	}
	return nil
}

// UpdateDoc partially updates specific fields of a document.
func (f *Firestore) UpdateDoc(ctx context.Context, path string, fields map[string]interface{}) error {
	encoded := encodeFields(fields)
	body, err := json.Marshal(map[string]interface{}{"fields": encoded})
	if err != nil {
		return fmt.Errorf("marshaling update: %w", err)
	}

	reqURL, err := url.Parse(f.docURL(path))
	if err != nil {
		return err
	}
	q := reqURL.Query()
	for field := range fields {
		q.Add("updateMask.fieldPaths", field)
	}
	reqURL.RawQuery = q.Encode()

	req, err := f.newRequest(ctx, "PATCH", reqURL.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("firestore update %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return f.responseError("update", path, resp)
	}
	return nil
}

// DeleteDoc removes a document.
func (f *Firestore) DeleteDoc(ctx context.Context, path string) error {
	req, err := f.newRequest(ctx, "DELETE", f.docURL(path), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("firestore delete %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		return f.responseError("delete", path, resp)
	}
	return nil
}

// ListDocs lists all documents in a collection.
// collectionPath format: "collection" or "collection/docID/subcollection".
func (f *Firestore) ListDocs(ctx context.Context, collectionPath string) ([]*Document, error) {
	var allDocs []*Document
	pageToken := ""

	for {
		reqURL, err := url.Parse(f.docURL(collectionPath))
		if err != nil {
			return nil, err
		}
		q := reqURL.Query()
		q.Set("pageSize", "300")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		reqURL.RawQuery = q.Encode()

		req, err := f.newRequest(ctx, "GET", reqURL.String(), nil)
		if err != nil {
			return nil, err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("firestore list %s: %w", collectionPath, err)
		}

		var result struct {
			Documents     []json.RawMessage `json:"documents"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			// Empty collection returns empty JSON or error.
			if len(allDocs) == 0 {
				return nil, nil
			}
			return allDocs, nil
		}
		resp.Body.Close()

		for _, raw := range result.Documents {
			doc, err := f.decodeDocumentBytes(raw)
			if err != nil {
				continue
			}
			allDocs = append(allDocs, doc)
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	return allDocs, nil
}

// ── Request helpers ─────────────────────────────────────────────────────────

func (f *Firestore) docURL(path string) string {
	return f.baseURL + "/" + strings.TrimPrefix(path, "/")
}

func (f *Firestore) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	token, err := f.auth.Token()
	if err != nil {
		return nil, fmt.Errorf("firestore auth: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

func (f *Firestore) responseError(op, path string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if len(body) > 0 {
		return fmt.Errorf("firestore %s %s: status %d: %s", op, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return fmt.Errorf("firestore %s %s: status %d", op, path, resp.StatusCode)
}

// ── Document decoding ───────────────────────────────────────────────────────

func (f *Firestore) decodeDocument(r io.Reader) (*Document, error) {
	var raw struct {
		Name       string                            `json:"name"`
		Fields     map[string]map[string]interface{} `json:"fields"`
		CreateTime string                            `json:"createTime"`
		UpdateTime string                            `json:"updateTime"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding document: %w", err)
	}

	return &Document{
		Name:       raw.Name,
		Fields:     decodeFields(raw.Fields),
		CreateTime: raw.CreateTime,
		UpdateTime: raw.UpdateTime,
	}, nil
}

func (f *Firestore) decodeDocumentBytes(data json.RawMessage) (*Document, error) {
	return f.decodeDocument(bytes.NewReader(data))
}

// ── Value encoding: Go → Firestore ─────────────────────────────────────────

func encodeFields(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = encodeValue(v)
	}
	return result
}

func encodeValue(v interface{}) interface{} {
	if v == nil {
		return map[string]interface{}{"nullValue": "NULL_VALUE"}
	}
	switch val := v.(type) {
	case string:
		return map[string]interface{}{"stringValue": val}
	case bool:
		return map[string]interface{}{"booleanValue": val}
	case int:
		return map[string]interface{}{"integerValue": strconv.Itoa(val)}
	case int64:
		return map[string]interface{}{"integerValue": strconv.FormatInt(val, 10)}
	case float64:
		return map[string]interface{}{"doubleValue": val}
	case time.Time:
		return map[string]interface{}{"timestampValue": val.Format(time.RFC3339Nano)}
	case []interface{}:
		values := make([]interface{}, len(val))
		for i, item := range val {
			values[i] = encodeValue(item)
		}
		return map[string]interface{}{"arrayValue": map[string]interface{}{"values": values}}
	case []map[string]interface{}:
		values := make([]interface{}, len(val))
		for i, item := range val {
			values[i] = encodeValue(item)
		}
		return map[string]interface{}{"arrayValue": map[string]interface{}{"values": values}}
	case map[string]interface{}:
		return map[string]interface{}{"mapValue": map[string]interface{}{"fields": encodeFields(val)}}
	default:
		// Fallback: convert to string.
		return map[string]interface{}{"stringValue": fmt.Sprint(val)}
	}
}

// ── Value decoding: Firestore → Go ─────────────────────────────────────────

func decodeFields(raw map[string]map[string]interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	result := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		result[k] = decodeValue(v)
	}
	return result
}

func decodeValue(v map[string]interface{}) interface{} {
	if val, ok := v["stringValue"]; ok {
		s, _ := val.(string)
		return s
	}
	if val, ok := v["integerValue"]; ok {
		switch n := val.(type) {
		case string:
			i, _ := strconv.ParseInt(n, 10, 64)
			return i
		case float64:
			return int64(n)
		}
	}
	if val, ok := v["doubleValue"]; ok {
		f, _ := val.(float64)
		return f
	}
	if val, ok := v["booleanValue"]; ok {
		b, _ := val.(bool)
		return b
	}
	if val, ok := v["timestampValue"]; ok {
		s, _ := val.(string)
		return s
	}
	if _, ok := v["nullValue"]; ok {
		return nil
	}
	if val, ok := v["arrayValue"]; ok {
		m, _ := val.(map[string]interface{})
		values, _ := m["values"].([]interface{})
		result := make([]interface{}, len(values))
		for i, item := range values {
			if itemMap, ok := item.(map[string]interface{}); ok {
				result[i] = decodeValue(itemMap)
			}
		}
		return result
	}
	if val, ok := v["mapValue"]; ok {
		m, _ := val.(map[string]interface{})
		fieldsRaw, _ := m["fields"].(map[string]interface{})
		// Convert to the expected type for decodeFields.
		typed := make(map[string]map[string]interface{}, len(fieldsRaw))
		for k, fv := range fieldsRaw {
			if fvm, ok := fv.(map[string]interface{}); ok {
				typed[k] = fvm
			}
		}
		return decodeFields(typed)
	}
	return nil
}

// ── Path helpers ────────────────────────────────────────────────────────────

// splitDocPath splits "collection/docID" into ("collection", "docID").
// For subcollections: "col/doc/subcol/subdoc" → ("col/doc/subcol", "subdoc").
func splitDocPath(path string) (collection, docID string) {
	path = strings.TrimPrefix(path, "/")
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path, ""
	}
	return path[:i], path[i+1:]
}
