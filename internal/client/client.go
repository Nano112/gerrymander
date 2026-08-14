// Package client is the thin Go client for the gerrymander API, shared by
// the CLI and the MCP server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a gerry API server.
type Client struct {
	Base   string // e.g. http://127.0.0.1:4780
	APIKey string
	HTTP   *http.Client
}

// New builds a client.
func New(base, apiKey string) *Client {
	return &Client{Base: base, APIKey: apiKey, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError carries the server's structured error body.
type APIError struct {
	Code        int
	Reason      string   `json:"error"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (%d): %s", e.Reason, e.Code, e.Message)
}

// Do performs a JSON request; out may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		apiErr := &APIError{Code: resp.StatusCode}
		json.NewDecoder(resp.Body).Decode(apiErr)
		if apiErr.Reason == "" {
			apiErr.Reason = resp.Status
		}
		return apiErr
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
