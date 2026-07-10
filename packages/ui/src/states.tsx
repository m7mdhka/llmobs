import * as React from "react";

export function Spinner({ label = "Loading" }: { label?: string }): React.ReactElement {
  return <span className="llm-spinner" role="status" aria-label={label} />;
}

export interface StateProps {
  title: string;
  body?: React.ReactNode;
  action?: React.ReactNode;
}

/** Centered loading state. */
export function LoadingState({ title = "Loading…" }: { title?: string }): React.ReactElement {
  return (
    <div className="llm-state" role="status">
      <Spinner />
      <div className="llm-state__title">{title}</div>
    </div>
  );
}

/** Centered empty state — no data yet, not an error. */
export function EmptyState({ title, body, action }: StateProps): React.ReactElement {
  return (
    <div className="llm-state">
      <div className="llm-state__title">{title}</div>
      {body && <div className="llm-state__body">{body}</div>}
      {action}
    </div>
  );
}

/** Centered error state with an optional retry action. */
export function ErrorState({ title, body, action }: StateProps): React.ReactElement {
  return (
    <div className="llm-state" role="alert">
      <div className="llm-state__title" style={{ color: "var(--llmobs-danger)" }}>
        {title}
      </div>
      {body && <div className="llm-state__body">{body}</div>}
      {action}
    </div>
  );
}

/** The standard state a plugin surface degrades to when its remote fails to load
 *  or the plugin is disabled/unavailable. The shell and SDK both render this so a
 *  broken plugin never takes down the app frame. */
export function PluginUnavailable({
  pluginName,
  reason,
  action,
}: {
  pluginName: string;
  reason?: React.ReactNode;
  action?: React.ReactNode;
}): React.ReactElement {
  return (
    <div className="llm-state" role="alert">
      <div className="llm-state__title">{pluginName} is unavailable</div>
      <div className="llm-state__body">
        {reason ?? "This plugin could not be loaded. The rest of the app is unaffected."}
      </div>
      {action}
    </div>
  );
}
