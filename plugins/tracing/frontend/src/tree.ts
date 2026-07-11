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

// MAX_TREE_DEPTH bounds attached nesting. A chain deeper than this is broken into
// roots so the recursive renderer can never overflow the stack; real traces are
// nowhere near this deep, so a chain this long signals a malformed producer.
const MAX_TREE_DEPTH = 1000;

// Build a parent→children tree from the flat (preorder) span list. Orphans and
// roots are top-level; input order is preserved for siblings (the API already
// returns tree order).
//
// The output forest is PROVABLY acyclic and depth-bounded. A `parent_span_id` that
// is self-referential, forms a cycle (A→B→A), or nests past MAX_TREE_DEPTH — a
// malformed or malicious producer; the kernel merges by id but does not validate the
// parent graph — would otherwise make the recursive renderer loop forever or blow the
// stack. Such a span is promoted to a root instead. Duplicate ids are de-duped
// (first wins), since the tree must never assume id uniqueness.
export function buildTree(spans: WireSpan[]): SpanNode[] {
  const nodes = new Map<string, SpanNode>();
  for (const s of spans) if (!nodes.has(s.id)) nodes.set(s.id, { span: s, depth: 0, children: [] });

  const roots: SpanNode[] = [];
  for (const s of spans) {
    const node = nodes.get(s.id)!;
    if (node.span !== s) continue; // a duplicate id we didn't keep
    const parent = resolveParent(s.id, nodes);
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  assignDepths(roots); // acyclic + bounded by construction — iterative, no recursion
  return roots;
}

// resolveParent returns a span's effective parent node, or null (→ root) when the
// parent is missing (orphan), is the span itself, or following the parent chain
// upward reveals a cycle or exceeds the depth cap. The upward walk carries a visited
// set so a pre-existing cycle terminates.
function resolveParent(id: string, nodes: Map<string, SpanNode>): SpanNode | null {
  const parentId = nodes.get(id)!.span.parent_span_id;
  const parent = parentId ? nodes.get(parentId) : undefined;
  if (!parent || parent.span.id === id) return null;
  const seen = new Set<string>([id]);
  let cur: SpanNode | undefined = parent;
  let steps = 0;
  while (cur !== undefined) {
    const curId: string = cur.span.id;
    if (seen.has(curId)) return null; // cycle
    if (++steps > MAX_TREE_DEPTH) return null; // too deep
    seen.add(curId);
    const nextId: string | undefined = cur.span.parent_span_id;
    cur = nextId ? nodes.get(nextId) : undefined;
  }
  return parent;
}

// assignDepths sets node.depth by an iterative DFS from the roots (the forest is
// acyclic here, so no visited set is needed and there is no recursion to overflow).
function assignDepths(roots: SpanNode[]): void {
  const stack: Array<{ node: SpanNode; depth: number }> = roots.map((node) => ({ node, depth: 0 }));
  while (stack.length > 0) {
    const { node, depth } = stack.pop()!;
    node.depth = depth;
    for (const c of node.children) stack.push({ node: c, depth: depth + 1 });
  }
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
