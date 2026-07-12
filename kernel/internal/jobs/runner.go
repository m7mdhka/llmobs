package jobs

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

// Runner invokes a plugin's job endpoint, presenting a kernel-signed SYSTEM
// identity assertion. A scheduled job runs user-less, so it skips the user half of
// the double-token intersection — but that half is replaced by the plugin's OWN
// grant, never god-mode: the assertion carries the plugin's declared permissions on
// the plugin's project, and a system actor so job-initiated access is
// audit-distinguishable from on-behalf-of-user access.
type Runner struct {
	signer      *plugintoken.Signer
	client      *http.Client
	ttl         time.Duration
	maxAttempts int
	backoff     time.Duration
}

// NewRunner builds a job runner. maxAttempts<=0 defaults to 3.
func NewRunner(signer *plugintoken.Signer, timeout time.Duration, maxAttempts int) *Runner {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	return &Runner{signer: signer, client: &http.Client{Timeout: timeout}, ttl: 5 * time.Minute, maxAttempts: maxAttempts, backoff: 500 * time.Millisecond}
}

// SystemActor renders the audit actor for a scheduled job.
func SystemActor(pluginID, job string) string { return "system:job:" + pluginID + ":" + job }

// systemAssertion mints the bounded system assertion: audience-bound to the
// plugin, scoped to the plugin's OWN permissions (pluginPerms — never All()), on
// the plugin's project, with a system actor.
func (r *Runner) systemAssertion(pluginID, projectID, job string, pluginPerms []string) (string, error) {
	actor := SystemActor(pluginID, job)
	tok, _, err := r.signer.MintIdentityAssertion(pluginID, actor, projectID, actor, pluginPerms, time.Now(), r.ttl)
	return tok, err
}

// invoke POSTs to the plugin's job endpoint with the system assertion, retrying on
// failure up to maxAttempts (or a per-job override). Returns the attempt count and
// any final error.
func (r *Runner) invoke(ctx context.Context, backendURL, path, assertion string, maxAttempts int) (int, error) {
	if maxAttempts <= 0 {
		maxAttempts = r.maxAttempts
	}
	url := strings.TrimRight(backendURL, "/") + "/" + strings.TrimLeft(path, "/")
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = r.once(ctx, url, assertion)
		if lastErr == nil {
			return attempt, nil
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return attempt, ctx.Err()
			case <-time.After(r.backoff * time.Duration(attempt)):
			}
		}
	}
	return maxAttempts, lastErr
}

func (r *Runner) once(ctx context.Context, url, assertion string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pluginproto.IdentityAssertionHeader, assertion)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("job endpoint returned %d", resp.StatusCode)
	}
	return nil
}
