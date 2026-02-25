// Package smithly provides a Go client for the Smithly sidecar API.
// Uses net/http + encoding/json only (stdlib).
package smithly

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

var (
	api   = envOr("SMITHLY_API", "http://localhost:18791")
	token = os.Getenv("SMITHLY_TOKEN")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func get(path string, result any) error {
	req, err := http.NewRequest("GET", api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return doRequest(req, result)
}

func post(path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", api+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doRequest(req, result)
}

func doRequest(req *http.Request, result any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sidecar %s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, body)
	}
	if result != nil {
		return json.Unmarshal(body, result)
	}
	return nil
}

// OAuthToken returns a fresh bearer token for the named OAuth2 provider.
func OAuthToken(provider string) (string, error) {
	var resp struct{ Token string }
	if err := get("/oauth2/"+provider, &resp); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// Notify sends a push notification.
func Notify(title, message string, priority int) error {
	var resp struct{ OK bool }
	return post("/notify", map[string]any{
		"title": title, "message": message, "priority": priority,
	}, &resp)
}

// Audit logs an audit entry.
func Audit(action, target, details string) error {
	var resp struct{ OK bool }
	return post("/audit", map[string]any{
		"action": action, "target": target, "details": details,
	}, &resp)
}

// Secret reads a secret by name from the sidecar. Value never touches env vars.
func Secret(name string) (string, error) {
	var resp struct{ Value string }
	if err := get("/secrets/"+name, &resp); err != nil {
		return "", err
	}
	return resp.Value, nil
}

// StoreObject represents a versioned object in the store.
type StoreObject struct {
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	Skill     string          `json:"skill"`
	Data      json.RawMessage `json:"data"`
	Public    bool            `json:"public"`
	Deleted   bool            `json:"deleted"`
	CreatedAt string          `json:"created_at"`
}

// StorePut creates a new version of an object.
func StorePut(typ string, data any, public bool) (*StoreObject, error) {
	var result StoreObject
	if err := post("/store/put", map[string]any{
		"type": typ, "data": data, "public": public,
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// StoreGet returns the latest version of an object by ID.
func StoreGet(id string) (*StoreObject, error) {
	var result StoreObject
	if err := post("/store/get", map[string]string{"id": id}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// StoreDelete soft-deletes an object.
func StoreDelete(id string) error {
	var resp struct{ OK bool }
	return post("/store/delete", map[string]string{"id": id}, &resp)
}

// StoreQuery queries objects by type and filters.
func StoreQuery(opts map[string]any) ([]StoreObject, error) {
	var result []StoreObject
	if err := post("/store/query", opts, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// StoreHistory returns all versions of an object.
func StoreHistory(id string) ([]StoreObject, error) {
	var result []StoreObject
	if err := post("/store/history", map[string]string{"id": id}, &result); err != nil {
		return nil, err
	}
	return result, nil
}
