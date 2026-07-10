package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// ExternalURL is the external-URL executor (R2): it reaches a plugin backend the
// operator runs, over plain HTTP GET, and never starts or stops a process.
type ExternalURL struct {
	client *http.Client
}

// NewExternalURL builds the executor with a per-probe timeout.
func NewExternalURL(timeout time.Duration) *ExternalURL {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &ExternalURL{client: &http.Client{Timeout: timeout}}
}

func (e *ExternalURL) Name() string { return "external-url" }

// Handshake calls GET {url}{infoPath} and decodes the plugin's self-report.
func (e *ExternalURL) Handshake(ctx context.Context, b Backend) (pluginproto.Info, error) {
	var info pluginproto.Info
	path := b.InfoPath
	if path == "" {
		path = pluginproto.DefaultInfoPath
	}
	if err := e.getJSON(ctx, joinURL(b.URL, path), &info); err != nil {
		return info, fmt.Errorf("handshake: %w", err)
	}
	return info, nil
}

// Health calls GET {url}{healthPath} and decodes the plugin's health report.
func (e *ExternalURL) Health(ctx context.Context, b Backend) (pluginproto.Health, error) {
	var h pluginproto.Health
	path := b.HealthPath
	if path == "" {
		path = pluginproto.DefaultHealthPath
	}
	if err := e.getJSON(ctx, joinURL(b.URL, path), &h); err != nil {
		return h, fmt.Errorf("health: %w", err)
	}
	return h, nil
}

// DeliverToken POSTs the service token to {url}/plugin/v1/token (H7c). This is the
// only token path — kernel-initiated push to the plugin's own registered URL.
func (e *ExternalURL) DeliverToken(ctx context.Context, b Backend, token string, expiresUnix int64) error {
	body, err := json.Marshal(pluginproto.TokenDelivery{ServiceToken: token, ExpiresUnix: expiresUnix})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(b.URL, pluginproto.DefaultTokenPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("deliver token: status %d", resp.StatusCode)
	}
	return nil
}

func (e *ExternalURL) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
