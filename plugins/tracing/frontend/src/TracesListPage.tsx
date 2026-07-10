import React, { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Button,
  EmptyState,
  ErrorState,
  Input,
  LoadingState,
  Mono,
  Num,
  Row,
  Table,
  useTraces,
  useQuery,
} from "@llmobs/plugin-sdk";
import { fmtDuration, fmtTime } from "./format.js";
import type { WireTrace } from "./wire.js";

// The Traces list — a filterable, paginated table over the `traces` DSL target.
// Data comes exclusively from the SDK's useTraces hook.
// A modest dogfood of the QD-4 aggregation path: span counts by kind over the
// window, rendered as a one-line strip (not a dashboard).
function SummaryStrip({ from, to }: { from: string; to: string }): React.ReactElement | null {
  const doc = useMemo(
    () => ({ target: "spans" as const, timeRange: { from, to }, groupBy: ["kind"], aggregations: [{ op: "count" }] }),
    [from, to],
  );
  const { data } = useQuery<{ g0: string; count: number }>(doc);
  if (!data || data.data.length === 0) return null;
  const total = data.data.reduce((n, r) => n + (r.count ?? 0), 0);
  return (
    <div className="tr-summary">
      <span className="tr-summary__total llmobs-tabular">{total}</span> spans ·{" "}
      {data.data.map((r) => (
        <span key={r.g0} className="tr-summary__kind">
          {r.g0} <b className="llmobs-tabular">{r.count}</b>
        </span>
      ))}
    </div>
  );
}

export function TracesListPage(): React.ReactElement {
  const navigate = useNavigate();
  const [env, setEnv] = useState("");
  const [pending, setPending] = useState("");

  // A generous default window; the kernel enforces the max.
  const { from, to } = useMemo(() => window7d(), []);
  const filters = useMemo(
    () => (env ? [{ field: "environment", op: "eq", value: env }] : undefined),
    [env],
  );

  const { data, loading, error, refetch } = useTraces({ from, to, filters, limit: 50 });

  if (loading && !data) return <LoadingState title="Loading traces…" />;
  if (error) {
    return (
      <ErrorState
        title="Couldn't load traces"
        body={error.message}
        action={<Button onClick={refetch}>Retry</Button>}
      />
    );
  }

  const traces = (data?.data ?? []) as unknown as WireTrace[];

  return (
    <div className="tr-page">
      <div className="tr-header">
        <h1 className="tr-title">Traces</h1>
        <form
          className="tr-filters"
          onSubmit={(e) => {
            e.preventDefault();
            setEnv(pending.trim());
          }}
        >
          <Input
            placeholder="Filter by environment…"
            value={pending}
            onChange={(e) => setPending(e.target.value)}
            aria-label="Filter by environment"
          />
          <Button type="submit" variant="secondary" size="sm">
            Apply
          </Button>
          {env && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setEnv("");
                setPending("");
              }}
            >
              Clear
            </Button>
          )}
        </form>
      </div>

      <SummaryStrip from={from} to={to} />
      {traces.length === 0 ? (
        <EmptyState
          title="No traces in this window"
          body="Emit a trace to your ingestion endpoint and it will appear here."
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Trace ID</th>
              <th>Started</th>
              <th>Duration</th>
              <th>Spans</th>
              <th>Status</th>
              <th>Environment</th>
              <th>Session</th>
            </tr>
          </thead>
          <tbody>
            {traces.map((t) => (
              <Row key={t.id} onActivate={() => navigate(t.id)}>
                <td>
                  {t.name || <span className="tr-muted">(unnamed)</span>}
                  {t["llmobs.dq.incomplete_trace"] && <span className="tr-incomplete" title="Some spans are missing (upstream sampling)">incomplete</span>}
                  {t.is_open && <span className="tr-open" title="Trace still active">open</span>}
                </td>
                <td>
                  <Mono>{t.id.slice(0, 12)}</Mono>
                </td>
                <td className="tr-nowrap">{fmtTime(t.start_time)}</td>
                <td>
                  <Num>{fmtDuration(t.start_time, t.end_time)}</Num>
                </td>
                <td>
                  <Num>{t.span_count ?? 0}</Num>
                </td>
                <td>
                  <StatusBadge code={t.status?.code} />
                </td>
                <td>
                  <Mono>{t.environment}</Mono>
                </td>
                <td>
                  <Mono>{t.session_id || "—"}</Mono>
                </td>
              </Row>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function StatusBadge({ code }: { code?: string }): React.ReactElement {
  const cls = code === "error" ? "tr-status-error" : code === "ok" ? "tr-status-ok" : "tr-status-unset";
  return <span className={cls}>{code || "unset"}</span>;
}

function window7d(): { from: string; to: string } {
  const now = Date.now();
  const day = 24 * 60 * 60 * 1000;
  return { from: new Date(now - 7 * day).toISOString(), to: new Date(now + day).toISOString() };
}
