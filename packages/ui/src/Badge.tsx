import * as React from "react";
import { hueForKind } from "@llmobs/tokens";
import { cx } from "./util.js";

export interface BadgeProps {
  children: React.ReactNode;
  /** Explicit color (CSS color/var). Overrides `kind`. */
  color?: string;
  /** Show a leading status dot. */
  dot?: boolean;
  className?: string;
}

/** A small, monospace status/label chip. Colored via a soft tinted surface. */
export function Badge({ children, color, dot, className }: BadgeProps): React.ReactElement {
  const c = color ?? "var(--llmobs-text-muted)";
  return (
    <span
      className={cx("llm-badge", className)}
      style={{ color: c, background: "color-mix(in srgb, currentColor 12%, transparent)", borderColor: "color-mix(in srgb, currentColor 24%, transparent)" }}
    >
      {dot && <span className="llm-badge__dot" />}
      {children}
    </span>
  );
}

/** A badge colored by canonical span kind. */
export function KindBadge({ kind }: { kind: string }): React.ReactElement {
  return (
    <Badge color={hueForKind(kind)} dot>
      {kind}
    </Badge>
  );
}
