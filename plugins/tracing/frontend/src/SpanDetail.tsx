import React from "react";
import { Tabs, type TabItem } from "@llmobs/plugin-sdk/react";
import type { WireSpan } from "./wire.js";
import { fmtDuration } from "./format.js";

// The span detail panel: overview plus tabs for payloads, attributes, usage, and
// span events. Everything shown comes from the span the SDK returned.
export function SpanDetail({ span }: { span: WireSpan }): React.ReactElement {
  const usage = { ...(span.provided_usage_details ?? {}), ...(span.usage_details ?? {}) };
  const hasUsage = Object.keys(usage).length > 0;
  const events = span.events ?? [];

  const tabs: TabItem[] = [
    {
      value: "payloads",
      label: "Payloads",
      content: (
        <div className="tr-kv">
          <Payload label="Input" value={span.input} />
          <Payload label="Output" value={span.output} />
        </div>
      ),
    },
    {
      value: "attributes",
      label: `Attributes${count(span.attributes)}`,
      content: <KeyValues data={span.attributes ?? {}} />,
    },
    {
      value: "usage",
      label: "Usage",
      content: hasUsage ? <KeyValues data={usage} /> : <Empty>No usage recorded.</Empty>,
    },
    {
      value: "events",
      label: `Events${events.length ? ` (${events.length})` : ""}`,
      content: events.length ? (
        <div className="tr-events">
          {events.map((e, i) => (
            <div className="tr-event" key={i}>
              <div className="tr-event__name">{e.name}</div>
              {e.attributes && Object.keys(e.attributes).length > 0 && <KeyValues data={e.attributes} dense />}
            </div>
          ))}
        </div>
      ) : (
        <Empty>No span events.</Empty>
      ),
    },
  ];

  return (
    <div className="tr-spandetail">
      <div className="tr-spandetail__head">
        <div className="tr-spandetail__name">{span.name || "(unnamed)"}</div>
        <dl className="tr-meta">
          <Meta k="kind" v={span.kind} mono />
          {span.model && <Meta k="model" v={span.model} mono />}
          {span.provider && <Meta k="provider" v={span.provider} mono />}
          <Meta k="duration" v={fmtDuration(span.start_time, span.end_time)} />
          <Meta k="span id" v={span.id} mono />
          {span.parent_span_id && <Meta k="parent" v={span.parent_span_id} mono />}
          {span.status?.code && <Meta k="status" v={span.status.code} mono />}
        </dl>
      </div>
      <Tabs items={tabs} />
    </div>
  );
}

function Payload({ label, value }: { label: string; value: unknown }): React.ReactElement {
  if (value === undefined || value === null || value === "") {
    return (
      <div className="tr-payload">
        <div className="tr-payload__label">{label}</div>
        <Empty>Empty.</Empty>
      </div>
    );
  }
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  return (
    <div className="tr-payload">
      <div className="tr-payload__label">{label}</div>
      <pre className="tr-pre">{text}</pre>
    </div>
  );
}

function KeyValues({ data, dense }: { data: Record<string, unknown>; dense?: boolean }): React.ReactElement {
  const keys = Object.keys(data).sort();
  if (keys.length === 0) return <Empty>None.</Empty>;
  return (
    <table className={"tr-kvtable" + (dense ? " tr-kvtable--dense" : "")}>
      <tbody>
        {keys.map((k) => (
          <tr key={k}>
            <td className="tr-kvtable__k">{k}</td>
            <td className="tr-kvtable__v">{renderVal(data[k])}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function renderVal(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function Meta({ k, v, mono }: { k: string; v: string; mono?: boolean }): React.ReactElement {
  return (
    <div className="tr-meta__row">
      <dt>{k}</dt>
      <dd className={mono ? "tr-mono" : undefined}>{v}</dd>
    </div>
  );
}

function Empty({ children }: { children: React.ReactNode }): React.ReactElement {
  return <div className="tr-empty">{children}</div>;
}

function count(obj?: Record<string, unknown>): string {
  const n = obj ? Object.keys(obj).length : 0;
  return n ? ` (${n})` : "";
}
