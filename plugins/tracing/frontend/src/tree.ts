import type { WireSpan } from "./wire.js";

export interface SpanNode {
  span: WireSpan;
  depth: number;
  children: SpanNode[];
}

export interface Bounds {
  start: number;
  end: number;
}

// The trace's overall time span, for laying out relative timing bars.
export function spanBounds(spans: WireSpan[]): Bounds | null {
  let start = Infinity;
  let end = -Infinity;
  for (const s of spans) {
    const st = s.start_time ? new Date(s.start_time).getTime() : NaN;
    const en = s.end_time ? new Date(s.end_time).getTime() : st;
    if (!Number.isNaN(st)) start = Math.min(start, st);
    if (!Number.isNaN(en)) end = Math.max(end, en);
  }
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;
  return { start, end };
}

// Build a parent→children tree from the flat (preorder) span list. Orphans and
// roots are top-level; input order is preserved for siblings (the API already
// returns tree order).
export function buildTree(spans: WireSpan[]): SpanNode[] {
  const nodes = new Map<string, SpanNode>();
  for (const s of spans) nodes.set(s.id, { span: s, depth: 0, children: [] });
  const roots: SpanNode[] = [];
  for (const s of spans) {
    const node = nodes.get(s.id)!;
    const parent = s.parent_span_id ? nodes.get(s.parent_span_id) : undefined;
    if (parent) {
      node.depth = parent.depth + 1;
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
}

// A span's position within the trace bounds, as [leftPct, widthPct].
export function barGeometry(span: WireSpan, bounds: Bounds): { left: number; width: number } {
  const total = bounds.end - bounds.start;
  const st = span.start_time ? new Date(span.start_time).getTime() : bounds.start;
  const en = span.end_time ? new Date(span.end_time).getTime() : st;
  const left = clampPct(((st - bounds.start) / total) * 100);
  const width = clampPct(((Math.max(en, st) - st) / total) * 100);
  return { left, width: Math.max(width, 1.5) };
}

function clampPct(n: number): number {
  if (Number.isNaN(n)) return 0;
  return Math.min(100, Math.max(0, n));
}
