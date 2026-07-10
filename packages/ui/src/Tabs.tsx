import * as React from "react";
import * as RadixTabs from "@radix-ui/react-tabs";

export interface TabItem {
  value: string;
  label: string;
  content: React.ReactNode;
}

export interface TabsProps {
  items: TabItem[];
  defaultValue?: string;
  value?: string;
  onValueChange?: (value: string) => void;
}

/** Keyboard-navigable tabs (Radix), styled from tokens. */
export function Tabs({ items, defaultValue, value, onValueChange }: TabsProps): React.ReactElement {
  return (
    <RadixTabs.Root
      defaultValue={defaultValue ?? items[0]?.value}
      value={value}
      onValueChange={onValueChange}
    >
      <RadixTabs.List className="llm-tabs-list">
        {items.map((it) => (
          <RadixTabs.Trigger key={it.value} value={it.value} className="llm-tab">
            {it.label}
          </RadixTabs.Trigger>
        ))}
      </RadixTabs.List>
      {items.map((it) => (
        <RadixTabs.Content key={it.value} value={it.value} style={{ paddingTop: "var(--llmobs-space-4)" }}>
          {it.content}
        </RadixTabs.Content>
      ))}
    </RadixTabs.Root>
  );
}
