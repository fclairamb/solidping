import * as React from "react";

import { cn } from "@/lib/utils";

// Lightweight, dependency-free in-page tabs. State is controlled by the parent
// via `value` / `onValueChange`. Mirrors the shadcn Tabs API surface
// (Tabs / TabsList / TabsTrigger / TabsContent) without pulling in Radix.

interface TabsContextValue {
  value: string;
  setValue: (value: string) => void;
}

const TabsContext = React.createContext<TabsContextValue | null>(null);

function useTabsContext(): TabsContextValue {
  const ctx = React.useContext(TabsContext);
  if (!ctx) {
    throw new Error("Tabs components must be used within <Tabs>");
  }
  return ctx;
}

export interface TabsProps {
  value: string;
  onValueChange: (value: string) => void;
  className?: string;
  children: React.ReactNode;
}

export function Tabs({ value, onValueChange, className, children }: TabsProps) {
  const ctx = React.useMemo<TabsContextValue>(
    () => ({ value, setValue: onValueChange }),
    [value, onValueChange],
  );
  return (
    <TabsContext.Provider value={ctx}>
      <div className={cn("flex flex-col gap-4", className)}>{children}</div>
    </TabsContext.Provider>
  );
}

export function TabsList({
  className,
  children,
}: {
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div
      role="tablist"
      className={cn(
        "inline-flex h-9 items-center justify-start gap-1 rounded-lg bg-muted p-1 text-muted-foreground",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function TabsTrigger({
  value,
  className,
  children,
  "data-testid": testId,
}: {
  value: string;
  className?: string;
  children: React.ReactNode;
  "data-testid"?: string;
}) {
  const { value: active, setValue } = useTabsContext();
  const selected = active === value;
  return (
    <button
      type="button"
      role="tab"
      aria-selected={selected}
      data-state={selected ? "active" : "inactive"}
      data-testid={testId}
      onClick={() => setValue(value)}
      className={cn(
        "inline-flex items-center justify-center whitespace-nowrap rounded-md px-3 py-1 text-sm font-medium ring-offset-background transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        selected
          ? "bg-card text-foreground shadow"
          : "text-muted-foreground hover:text-foreground",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function TabsContent({
  value,
  className,
  children,
  "data-testid": testId,
}: {
  value: string;
  className?: string;
  children: React.ReactNode;
  "data-testid"?: string;
}) {
  const { value: active } = useTabsContext();
  if (active !== value) return null;
  return (
    <div
      role="tabpanel"
      data-testid={testId}
      className={cn("focus-visible:outline-none", className)}
    >
      {children}
    </div>
  );
}
