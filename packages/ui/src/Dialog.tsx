import * as React from "react";
import * as RadixDialog from "@radix-ui/react-dialog";

export interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: React.ReactNode;
}

/** A modal dialog (Radix Dialog, owned + styled from tokens). Focus-trapped and
 *  dismissible; the title labels the dialog for assistive tech. */
export function Dialog({ open, onOpenChange, title, children }: DialogProps): React.ReactElement {
  return (
    <RadixDialog.Root open={open} onOpenChange={onOpenChange}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="llm-dialog-overlay" />
        <RadixDialog.Content className="llm-dialog">
          <RadixDialog.Title
            style={{
              margin: 0,
              marginBottom: "var(--llmobs-space-4)",
              fontSize: "var(--llmobs-text-lg)",
              fontWeight: 600,
            }}
          >
            {title}
          </RadixDialog.Title>
          {children}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}
