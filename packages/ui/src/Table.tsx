import * as React from "react";
import { cx } from "./util.js";

export interface TableProps extends React.TableHTMLAttributes<HTMLTableElement> {
  density?: "compact" | "cozy";
}

/** A horizontally-scrollable data table. Wrap header cells in <Th>, body in rows. */
export function Table({ density = "cozy", className, children, ...props }: TableProps): React.ReactElement {
  return (
    <div className="llm-table-wrap">
      <table className={cx("llm-table", density === "compact" && "llm-table--compact", className)} {...props}>
        {children}
      </table>
    </div>
  );
}

export interface RowProps extends React.HTMLAttributes<HTMLTableRowElement> {
  onActivate?: () => void;
}

/** A table row that is keyboard-activatable when onActivate is provided. */
export function Row({ onActivate, children, ...props }: RowProps): React.ReactElement {
  const clickable = Boolean(onActivate);
  return (
    <tr
      data-clickable={clickable}
      tabIndex={clickable ? 0 : undefined}
      role={clickable ? "button" : undefined}
      onClick={onActivate}
      onKeyDown={
        clickable
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onActivate?.();
              }
            }
          : undefined
      }
      {...props}
    >
      {children}
    </tr>
  );
}

export function Num({ children, className }: { children: React.ReactNode; className?: string }): React.ReactElement {
  return <span className={cx("llm-num", "llmobs-tabular", className)}>{children}</span>;
}

export function Mono({ children, className }: { children: React.ReactNode; className?: string }): React.ReactElement {
  return <span className={cx("llm-mono", className)}>{children}</span>;
}
