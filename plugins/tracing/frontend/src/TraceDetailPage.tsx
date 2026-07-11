import React, { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Button, ErrorState, LoadingState, useTrace } from "@llmobs/plugin-sdk/react";
import type { WireSpan, WireTraceTree } from "./wire.js";
import { buildTree, spanBounds, type SpanNode } from "./tree.js";
import { SpanRow } from "./SpanRow.js";
import { SpanDetail } from "./SpanDetail.js";

// A trace's span tree (from the tree endpoint) plus a detail panel. The tree is
// laid out with timing bars relative to the whole trace's span; selecting a span
// opens its payloads/attributes/usage/events.
export function TraceDetailPage(): React.ReactElement {
  const { traceId } = useParams<{ traceId: string }>();
  const navigate = useNavigate();
  const { data, loading, error, refetch } = useTrace(traceId ?? null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());

  const tree = data as unknown as WireTraceTree | null;
  const bounds = useMemo(() => (tree ? spanBounds(tree.spans) : null), [tree]);
  const roots = useMemo(() => (tree ? buildTree(tree.spans) : []), [tree]);
  const byId = useMemo(() => {
    const m = new Map<string, WireSpan>();
    tree?.spans.forEach((s) => m.set(s.id, s));
    return m;
  }, [tree]);

  if (loading && !tree) return <LoadingState title="Loading trace…" />;
  if (error) {
    return (
      <ErrorState
        title="Couldn't load trace"
        body={error.message}
        action={
          <div className="tr-actions">
            <Button onClick={refetch}>Retry</Button>
            <Button variant="ghost" onClick={() => navigate("..")}>
              Back to traces
            </Button>
          </div>
        }
      />
    );
  }
  if (!tree) return <ErrorState title="Trace not found" />;

  const selected = selectedId ? byId.get(selectedId) ?? null : null;

  function toggle(id: string): void {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  const flat = flatten(roots, collapsed);

  return (
    <div className="tr-detail">
      <div className="tr-detail__head">
        <Button variant="ghost" size="sm" onClick={() => navigate("..")}>
          ← Traces
        </Button>
        <div className="tr-detail__title">
          <span className="tr-detail__name">{tree.trace.name || "(unnamed trace)"}</span>
          <span className="tr-detail__id">{tree.trace.id}</span>
        </div>
        <span className="tr-detail__count">{tree.spans.length} spans</span>
      </div>

      <div className="tr-detail__body">
        <div className="tr-tree" role="tree" aria-label="Span tree">
          {flat.map((node) => (
            <SpanRow
              key={node.span.id}
              node={node}
              bounds={bounds}
              selected={node.span.id === selectedId}
              collapsed={collapsed.has(node.span.id)}
              onSelect={() => setSelectedId(node.span.id)}
              onToggle={() => toggle(node.span.id)}
            />
          ))}
        </div>
        <aside className="tr-panel" aria-label="Span detail">
          {selected ? (
            <SpanDetail span={selected} />
          ) : (
            <div className="tr-panel__empty">Select a span to inspect its payloads, attributes, usage, and events.</div>
          )}
        </aside>
      </div>
    </div>
  );
}

// Depth-first flatten honoring collapsed nodes, preserving the tree order.
function flatten(roots: SpanNode[], collapsed: Set<string>): SpanNode[] {
  const out: SpanNode[] = [];
  const walk = (n: SpanNode): void => {
    out.push(n);
    if (!collapsed.has(n.span.id)) n.children.forEach(walk);
  };
  roots.forEach(walk);
  return out;
}
