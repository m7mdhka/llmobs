package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/perm"
	"github.com/m7mdhka/llmobs/kernel/internal/controlplane/plugintoken"
	"github.com/m7mdhka/llmobs/kernel/internal/plugintest"
	"github.com/m7mdhka/llmobs/kernel/pkg/pluginproto"
)

type staticSource struct{ pj []PluginJobs }

func (s staticSource) PluginJobs() []PluginJobs { return s.pj }

func newScheduler(t *testing.T, fake *plugintest.FakeBackend, perms []string) (*Scheduler, *plugintoken.Signer, *MemStore) {
	t.Helper()
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	src := staticSource{pj: []PluginJobs{{
		PluginID: "acme/w", BackendURL: fake.URL(), Permissions: perms, Running: true,
		Jobs: []JobSpec{{Name: "nightly", Schedule: "@every 1h", Path: "/job", MaxAttempts: 3}},
	}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ms := NewMemStore()
	runner := NewRunner(signer, 2*time.Second, 3)
	runner.backoff = 2 * time.Millisecond // fast retries for tests
	return New(src, runner, ms, "proj_default", log, nil), signer, ms
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestScheduledRunSucceeds: a due job is dispatched, invokes the plugin endpoint,
// and records a succeeded run.
func TestScheduledRunSucceeds(t *testing.T) {
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/w"})
	defer fake.Close()
	s, _, ms := newScheduler(t, fake, []string{perm.TracesReadMetadata})
	s.Tick(context.Background())
	waitFor(t, func() bool { return fake.JobCalls() >= 1 })
	waitFor(t, func() bool {
		runs, _ := ms.RecentRuns(context.Background(), "acme/w", 10)
		return len(runs) == 1 && runs[0].Status == "succeeded" && runs[0].Trigger == "schedule"
	})
}

// TestFailedJobRetriesThenFails: a failing endpoint is retried up to maxAttempts,
// then the run is recorded failed with the attempt count.
func TestFailedJobRetriesThenFails(t *testing.T) {
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/w"})
	defer fake.Close()
	fake.SetJobFail(true)
	s, _, ms := newScheduler(t, fake, []string{perm.TracesReadMetadata})
	s.Tick(context.Background())
	waitFor(t, func() bool {
		runs, _ := ms.RecentRuns(context.Background(), "acme/w", 10)
		return len(runs) == 1 && runs[0].Status == "failed" && runs[0].Attempts == 3
	})
	if fake.JobCalls() != 3 {
		t.Fatalf("expected 3 attempts, got %d", fake.JobCalls())
	}
}

// TestLongRunningNotRetriggered: while a run is in flight, a second tick must NOT
// start another run of the same job.
func TestLongRunningNotRetriggered(t *testing.T) {
	fake := plugintest.NewFakeBackend(pluginproto.Info{ID: "acme/w"})
	defer fake.Close()
	s, _, ms := newScheduler(t, fake, []string{perm.TracesReadMetadata})
	// Manually mark a run in flight.
	_ = ms.StartRun(context.Background(), Run{ID: "run_x", PluginID: "acme/w", Job: "nightly", Trigger: "schedule", Actor: "system", Status: "running", StartedAt: time.Now()})
	s.Tick(context.Background())
	// No new invocation while running.
	time.Sleep(30 * time.Millisecond)
	if fake.JobCalls() != 0 {
		t.Fatalf("a running job must not be re-triggered, got %d calls", fake.JobCalls())
	}
}

// TestSystemAssertionBoundedToGrant is the prove-the-negative: the system
// assertion the runner presents is scoped to the plugin's OWN permissions on its
// project, with a system actor — never god-mode. A job CANNOT exceed the plugin's
// grant or reach another project through it.
func TestSystemAssertionBoundedToGrant(t *testing.T) {
	signer, err := plugintoken.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(signer, time.Second, 1)
	// Plugin granted ONLY metadata read.
	granted := []string{perm.TracesReadMetadata}
	asr, err := runner.systemAssertion("acme/w", "projA", "nightly", granted)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := signer.VerifyIdentityAssertion(context.Background(), asr, pluginproto.PluginSubject("acme/w"), time.Now())
	if err != nil {
		t.Fatalf("assertion should verify for its plugin: %v", err)
	}
	// Bounded to the plugin's grant — NOT god-mode.
	if len(claims.Scopes) != 1 || claims.Scopes[0] != perm.TracesReadMetadata {
		t.Fatalf("system assertion must carry ONLY the plugin grant, got %v", claims.Scopes)
	}
	if perm.Has(claims.Scopes, perm.TracesReadPayloads) || perm.Has(claims.Scopes, perm.ScoresWrite) {
		t.Fatal("system assertion must not exceed the plugin's grant")
	}
	// Pinned to the plugin's project — cannot reach another tenant.
	if claims.ProjectID != "projA" {
		t.Fatalf("system assertion must be pinned to the plugin's project, got %s", claims.ProjectID)
	}
	// Audit-distinguishable: a system actor, not a user session.
	if claims.Actor != "system:job:acme/w:nightly" || claims.Sub != claims.Actor {
		t.Fatalf("job identity must be audit-distinguishable as system-initiated, got actor=%q sub=%q", claims.Actor, claims.Sub)
	}
}
