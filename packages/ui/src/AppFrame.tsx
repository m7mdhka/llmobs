import * as React from "react";
import { cx } from "./util.js";

/** The application shell layout: a topbar spanning the top, a nav rail on the
 *  left, and a scrollable content region. The shell composes these; plugins
 *  render into the content region only. */
export function AppFrame({ children }: { children: React.ReactNode }): React.ReactElement {
  return <div className="llm-frame">{children}</div>;
}

export interface TopbarProps {
  brand?: React.ReactNode;
  children?: React.ReactNode;
}

export function Topbar({ brand, children }: TopbarProps): React.ReactElement {
  return (
    <header className="llm-topbar">
      <div className="llm-brand">
        <span className="llm-brand__mark" aria-hidden />
        {brand}
      </div>
      <div className="llm-topbar__spacer" />
      {children}
    </header>
  );
}

export function NavRail({ children }: { children: React.ReactNode }): React.ReactElement {
  return (
    <nav className="llm-navrail" aria-label="Primary">
      {children}
    </nav>
  );
}

export function NavSection({ children }: { children: React.ReactNode }): React.ReactElement {
  return <div className="llm-nav-section">{children}</div>;
}

export interface NavItemProps {
  active?: boolean;
  onSelect?: () => void;
  href?: string;
  children: React.ReactNode;
}

export function NavItem({ active, onSelect, href, children }: NavItemProps): React.ReactElement {
  return (
    <a
      className={cx("llm-nav-item")}
      data-active={active ? "true" : undefined}
      aria-current={active ? "page" : undefined}
      href={href ?? "#"}
      onClick={(e) => {
        if (onSelect) {
          e.preventDefault();
          onSelect();
        }
      }}
    >
      {children}
    </a>
  );
}

export function Content({ children }: { children: React.ReactNode }): React.ReactElement {
  return <main className="llm-content">{children}</main>;
}
