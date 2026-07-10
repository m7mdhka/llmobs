// The settings JSON Schema subset (ADR-0024) — the SAME subset the kernel validator
// accepts (kernel/internal/pluginsettings). Client and kernel must agree, so keep
// this in lockstep with the Go model.

export type FieldType = "string" | "number" | "integer" | "boolean";

export interface Field {
  name: string;
  type: FieldType;
  title?: string;
  description?: string;
  enum?: string[];
  /** writeOnly: true — a secret. Write-only: never rendered back, only "set" state. */
  secret: boolean;
  required: boolean;
  default?: unknown;
  minLength?: number;
  minimum?: number;
  maximum?: number;
}

export interface Model {
  fields: Field[];
}

interface PropSpec {
  type?: string;
  title?: string;
  description?: string;
  enum?: string[];
  writeOnly?: boolean;
  default?: unknown;
  minLength?: number;
  minimum?: number;
  maximum?: number;
}

interface RawSchema {
  type?: string;
  required?: string[];
  properties?: Record<string, PropSpec>;
}

const SUPPORTED: FieldType[] = ["string", "number", "integer", "boolean"];

/**
 * parseSchema distills the JSON Schema subset into a Model. It throws on anything
 * outside the subset (root not an object, unsupported field type, enum/secret on a
 * non-string) — the same failures the kernel raises, so a bad schema fails the same
 * way on both sides. Fields are sorted by name for a stable, deterministic order.
 */
export function parseSchema(raw: unknown): Model {
  const rs = (raw ?? {}) as RawSchema;
  if (rs.type && rs.type !== "object") {
    throw new Error(`settings schema root must be type object, got ${rs.type}`);
  }
  const required = new Set(rs.required ?? []);
  const fields: Field[] = [];
  for (const [name, p] of Object.entries(rs.properties ?? {})) {
    const type = p.type as FieldType;
    if (!SUPPORTED.includes(type)) {
      throw new Error(`settings field "${name}" has unsupported type "${p.type ?? ""}"`);
    }
    if (p.enum && p.enum.length > 0 && type !== "string") {
      throw new Error(`settings field "${name}": enum is only supported for string`);
    }
    if (p.writeOnly && type !== "string") {
      throw new Error(`settings field "${name}": writeOnly (secret) is only supported for string`);
    }
    fields.push({
      name,
      type,
      title: p.title,
      description: p.description,
      enum: p.enum,
      secret: !!p.writeOnly,
      required: required.has(name),
      default: p.default,
      minLength: p.minLength,
      minimum: p.minimum,
      maximum: p.maximum,
    });
  }
  fields.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return { fields };
}
