import React from "react";
import { EmptyState, LoadingState, ErrorState, useLLMObs, useTraces } from "@llmobs/plugin-sdk";

// The exposed surface. The shell mounts it at your nav path ("/your-plugin/*").
// Read all data through the SDK (the dogfood rule) — never reach around it.
export default function YourPlugin(): React.ReactElement {
  const { user } = useLLMObs();
  const now = Date.now();
  const day = 24 * 60 * 60 * 1000;
  const { data, loading, error, refetch } = useTraces({
    from: new Date(now - 7 * day).toISOString(),
    to: new Date(now + day).toISOString(),
    limit: 10,
  });

  if (loading && !data) return <LoadingState title="Loading…" />;
  if (error) return <ErrorState title="Couldn't load data" body={error.message} />;

  const count = data?.data.length ?? 0;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--llmobs-space-4)" }}>
      <h1 style={{ fontSize: "var(--llmobs-text-xl)", fontWeight: 600 }}>Your Plugin</h1>
      <p style={{ color: "var(--llmobs-text-muted)" }}>
        Signed in as {user?.email ?? "unknown"}. This starter queried the traces target and found {count} trace(s).
      </p>
      {count === 0 && <EmptyState title="No data yet" body="Replace this with your surface." />}
      <button onClick={refetch} style={{ alignSelf: "start" }}>
        Refresh
      </button>
    </div>
  );
}
