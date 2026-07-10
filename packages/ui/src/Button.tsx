import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cx } from "./util.js";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: "sm" | "md";
  /** Render as the single child element (Radix Slot) instead of a <button>. */
  asChild?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ variant = "secondary", size = "md", asChild, className, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        ref={ref}
        className={cx("llm-btn", `llm-btn--${variant}`, size === "sm" && "llm-btn--sm", className)}
        {...props}
      />
    );
  },
);
Button.displayName = "Button";
