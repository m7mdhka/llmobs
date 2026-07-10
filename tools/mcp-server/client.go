package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// llmobsClient is a tiny read-only client over the LLMObs Query API. It carries a
// metadata-scoped API key; the kernel enforces payload redaction server-side
// (PR-F2), so nothing this client fetches contains prompt/completion payloads.
type llmobsClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newClient(baseURL, apiKey string) *llmobsClient {
	return &llmobsClient{baseURL: baseURL, apiKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
}

type whoami struct {
	ProjectID    string   `json:"project_id"`
	Scopes       []string `json:"scopes"`
	ReadPayloads bool     `json:"read_payloads"`
}

func (c *llmobsClient) whoami(ctx context.Context) (whoami, error) {
	var w whoami
	err := c.do(ctx, http.MethodGet, "/v1alpha1/whoami", nil, &w)
	return w, err
}

type queryResponse struct {
	Data   []json.RawMessage `json:"data"`
	Cursor string            `json:"cursor,omitempty"`
	Stats  map[string]any    `json:"stats"`
}

func (c *llmobsClient) query(ctx context.Context, doc map[string]any) (*queryResponse, error) {
	var qr queryResponse
	if err := c.do(ctx, http.MethodPost, "/v1alpha1/query", doc, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}

func (c *llmobsClient) traceTree(ctx context.Context, traceID string) (map[string]any, error) {
	var t map[string]any
	err := c.do(ctx, http.MethodGet, "/v1alpha1/traces/"+urlEscape(traceID)+"/tree", nil, &t)
	return t, err
}

func (c *llmobsClient) span(ctx context.Context, id string) (map[string]any, error) {
	var s map[string]any
	err := c.do(ctx, http.MethodGet, "/v1alpha1/spans/"+urlEscape(id), nil, &s)
	return s, err
}

func (c *llmobsClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		return fmt.Errorf("llmobs %s %s: %d %s", method, path, res.StatusCode, string(data))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
