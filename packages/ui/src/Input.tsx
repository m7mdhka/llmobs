import * as React from "react";
import { cx } from "./util.js";

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  mono?: boolean;
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ mono, className, ...props }, ref) => (
    <input ref={ref} className={cx("llm-input", mono && "llm-input--mono", className)} {...props} />
  ),
);
Input.displayName = "Input";

export interface FieldProps {
  label: string;
  htmlFor?: string;
  children: React.ReactNode;
}

/** A labelled form field — the label is associated for accessibility. */
export function Field({ label, htmlFor, children }: FieldProps): React.ReactElement {
  return (
    <div className="llm-field">
      <label className="llm-label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
    </div>
  );
}
