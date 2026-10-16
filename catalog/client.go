package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the public Go SDK for catalog consumers and publishers.
// Keep credentials server-side; catalog-read credentials cannot modify Gerry.
type Client struct {
	BaseURL, Token string
	HTTP           *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type APIError struct {
	Status  int
	Reason  string `json:"error"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("catalog %s (%d): %s", e.Reason, e.Status, e.Message)
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode}
		json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(e)
		return e
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024)).Decode(out)
}

func (c *Client) Snapshot(ctx context.Context, applicationID string) (Snapshot, error) {
	var result Snapshot
	err := c.request(ctx, "GET", "/v1/catalog?application_id="+url.QueryEscape(applicationID), nil, &result)
	if err == nil && result.SchemaVersion != SchemaVersion {
		return Snapshot{}, fmt.Errorf("unsupported catalog schema version %d", result.SchemaVersion)
	}
	return result, err
}
func (c *Client) Access(ctx context.Context, environment, workspace string) ([]AccessEntry, error) {
	if !ValidID(environment) || (workspace != "" && !ValidID(workspace)) {
		return nil, fmt.Errorf("valid environment and workspace required")
	}
	var result struct {
		SchemaVersion int           `json:"schema_version"`
		Services      []AccessEntry `json:"services"`
	}
	err := c.request(ctx, "GET", "/v1/catalog/access?"+url.Values{"environment": {environment}, "workspace": {workspace}}.Encode(), nil, &result)
	if err == nil && (result.SchemaVersion != SchemaVersion || result.Services == nil) {
		return nil, fmt.Errorf("unsupported or invalid access catalog response")
	}
	return result.Services, err
}
func (c *Client) Apply(ctx context.Context, b Bundle) error {
	if err := b.Validate(); err != nil {
		return err
	}
	return c.request(ctx, "POST", "/v1/catalog/apply", b, nil)
}
func (c *Client) UploadAsset(ctx context.Context, data []byte) (Asset, error) {
	a, err := NewAsset(data)
	if err != nil {
		return Asset{}, err
	}
	var result Asset
	err = c.request(ctx, "POST", "/v1/catalog/assets", map[string]any{"data": data}, &result)
	if err == nil && result.ID != a.ID {
		return Asset{}, fmt.Errorf("asset digest mismatch")
	}
	return result, err
}
func (c *Client) Deactivate(ctx context.Context, deploymentID string) error {
	if !ValidID(deploymentID) {
		return fmt.Errorf("invalid deployment id")
	}
	return c.request(ctx, "POST", "/v1/catalog/deployments/"+url.PathEscape(deploymentID)+"/deactivate", nil, nil)
}
