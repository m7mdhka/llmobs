// Client-side settings validation — mirrors kernel/internal/pluginsettings/validate.go
// so the user sees the same errors the kernel would return. The kernel re-validates
// (never trust the client); this is for immediate feedback.

import type { Field, Model } from "./schema.js";

/** Field name → error message, for fields that failed validation. */
export type Errors = Record<string, string>;

/**
 * validate checks submitted values against the model. `alreadySet` names secret
 * fields that already have a stored value — a required secret is satisfied if it is
 * being provided now OR was previously set. An empty-string secret is the "preserve"
 * signal (treated as absent), matching the kernel.
 */
export function validate(
  model: Model,
  values: Record<string, unknown>,
  alreadySet: Record<string, boolean>,
): Errors {
  const errors: Errors = {};
  for (const f of model.fields) {
    let raw = values[f.name];
    let present = raw !== undefined && raw !== null;
    if (f.secret && present && raw === "") present = false; // empty secret = preserve
    if (!present) {
      if (f.required && !(f.secret && alreadySet[f.name])) {
        errors[f.name] = "Required.";
      }
      continue;
    }
    const msg = validateValue(f, raw);
    if (msg) errors[f.name] = msg;
  }
  return errors;
}

function validateValue(f: Field, raw: unknown): string | null {
  switch (f.type) {
    case "string": {
      if (typeof raw !== "string") return "Must be text.";
      if (f.minLength !== undefined && raw.length < f.minLength) return `Must be at least ${f.minLength} characters.`;
      if (f.enum && f.enum.length > 0 && !f.enum.includes(raw)) return "Must be one of the allowed values.";
      return null;
    }
    case "boolean":
      return typeof raw === "boolean" ? null : "Must be true or false.";
    case "number":
    case "integer": {
      const n = typeof raw === "number" ? raw : Number(raw);
      if (Number.isNaN(n)) return "Must be a number.";
      if (f.type === "integer" && !Number.isInteger(n)) return "Must be a whole number.";
      if (f.minimum !== undefined && n < f.minimum) return `Must be ≥ ${f.minimum}.`;
      if (f.maximum !== undefined && n > f.maximum) return `Must be ≤ ${f.maximum}.`;
      return null;
    }
    default:
      return "Unsupported field type.";
  }
}

export function hasErrors(e: Errors): boolean {
  return Object.keys(e).length > 0;
}
