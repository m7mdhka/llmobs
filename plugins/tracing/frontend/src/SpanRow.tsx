import React from "react";
import { KindBadge } from "@llmobs/plugin-sdk/react";
import { hueForKind } from "@llmobs/tokens";
import type { SpanNode, Bounds } from "./tree.js";
import { barGeometry } from "./tree.js";
import { fmtDuration } from "./format.js";

// One row in the span tree: indentation by depth, a collapse toggle, the kind
// badge, the name, and a timing bar positioned within the trace bounds.
export function SpanRow({
  node,
  bounds,
  selected,
  collapsed,
  onSelect,
  onToggle,
}: {
  node: SpanNode;
  bounds: Bounds | null;
  selected: boolean;
  collapsed: boolean;
  onSelect: () => void;
  onToggle: () => void;
}): React.ReactElement {
  const { span, depth, children } = node;
  const hasChildren = children.length > 0;
  const geo = bounds ? barGeometry(span, bounds) : { left: 0, width: 100 };
  const isError = span.status?.code === "error";

  return (
    <div
      className={"tr-span" + (selected ? " tr-span--selected" : "")}
      role="treeitem"
      aria-selected={selected}
      aria-expanded={hasChildren ? !collapsed : undefined}
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect();
        }
      }}
    >
      <div className="tr-span__label" style={{ paddingLeft: `${depth * 16}px` }}>
        {hasChildren ? (
          <button
            className="tr-span__toggle"
            aria-label={collapsed ? "Expand" : "Collapse"}
            onClick={(e) => {
              e.stopPropagation();
              onToggle();
            }}
          >
            {collapsed ? "▶" : "▼"}
          </button>
        ) : (
          <span className="tr-span__toggle tr-span__toggle--leaf" />
        )}
        <KindBadge kind={span.kind} />
        <span className="tr-span__name">{span.name || "(unnamed)"}</span>
        {isError && <span className="tr-span__err">error</span>}
      </div>
      <div className="tr-span__timeline">
        <div
          className="tr-span__bar"
          style={{
            left: `${geo.left}%`,
            width: `${geo.width}%`,
            background: hueForKind(span.kind),
          }}
          title={fmtDuration(span.start_time, span.end_time)}
        />
      </div>
      <div className="tr-span__dur">{fmtDuration(span.start_time, span.end_time)}</div>
    </div>
  );
}
