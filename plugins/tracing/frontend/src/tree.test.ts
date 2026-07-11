import { describe, it, expect } from "vitest";
import { buildTree, type SpanNode } from "./tree.js";
import type { WireSpan } from "./wire.js";

// A minimal WireSpan for tree-shape tests (only id/parent matter here).
function span(id: string, parent?: string): WireSpan {
  return { id, trace_id: "t", name: id, parent_span_id: parent } as WireSpan;
}

// Count nodes reachable from the roots with our OWN visited guard — if buildTree
// ever produced a cycle, this traversal (not the recursive renderer) is what would
// catch it without hanging.
function reachable(roots: SpanNode[]): number {
  const seen = new Set<SpanNode>();
  const stack = [...roots];
  let guard = 0;
  while (stack.length && guard++ < 1_000_000) {
    const n = stack.pop()!;
    if (seen.has(n)) continue;
    seen.add(n);
    stack.push(...n.children);
  }
  return seen.size;
}

describe("buildTree", () => {
  it("builds a normal parent→child tree", () => {
    const roots = buildTree([span("a"), span("b", "a"), span("c", "b")]);
    expect(roots).toHaveLength(1);
    expect(roots[0].span.id).toBe("a");
    expect(roots[0].children[0].span.id).toBe("b");
    expect(roots[0].children[0].children[0].span.id).toBe("c");
    expect(roots[0].children[0].children[0].depth).toBe(2);
  });

  it("treats a dropped-parent (orphan) span as a root", () => {
    // b's parent 'missing' isn't in the set — b is a root, not lost.
    const roots = buildTree([span("b", "missing"), span("c", "b")]);
    expect(roots.map((r) => r.span.id)).toContain("b");
    expect(reachable(roots)).toBe(2); // b and c both present
  });

  it("does not loop or lose spans on a 2-cycle (A→B→A)", () => {
    const roots = buildTree([span("a", "b"), span("b", "a")]);
    // The cycle is broken: both become roots, forest is acyclic, both reachable.
    expect(reachable(roots)).toBe(2);
    expect(roots.length).toBeGreaterThanOrEqual(1);
  });

  it("does not loop on a self-parent (A→A)", () => {
    const roots = buildTree([span("a", "a")]);
    expect(roots).toHaveLength(1);
    expect(roots[0].children).toHaveLength(0);
  });

  it("breaks an over-deep chain so depth stays bounded (no stack overflow on render)", () => {
    const spans: WireSpan[] = [span("n0")];
    for (let i = 1; i < 5000; i++) spans.push(span(`n${i}`, `n${i - 1}`));
    const roots = buildTree(spans);
    // Every span is present exactly once...
    expect(reachable(roots)).toBe(5000);
    // ...and no node is nested past the cap (depth ≤ 1000), so a recursive renderer
    // walks at most ~1000 levels, not 5000.
    let maxDepth = 0;
    const stack = [...roots];
    while (stack.length) {
      const n = stack.pop()!;
      maxDepth = Math.max(maxDepth, n.depth);
      stack.push(...n.children);
    }
    expect(maxDepth).toBeLessThanOrEqual(1000);
  });

  it("de-dupes colliding ids (first wins), never assuming id uniqueness", () => {
    const roots = buildTree([span("a"), span("a"), span("b", "a")]);
    // Only one 'a' node; b attaches to it.
    expect(reachable(roots)).toBe(2);
  });
});
