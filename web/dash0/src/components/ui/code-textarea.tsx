import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * A monospace textarea for editing code (CSS, JSON, snippets).
 *
 * Everything the browser normally does to prose — spell-checking, capitalizing
 * the first letter, autocorrecting, autocompleting — is wrong for code, so it
 * is all disabled here. Otherwise it is the plain Textarea look, so the two sit
 * side by side in a form without visual drift.
 */
const CodeTextarea = React.forwardRef<
  HTMLTextAreaElement,
  React.ComponentProps<"textarea">
>(({ className, ...props }, ref) => {
  return (
    <textarea
      ref={ref}
      spellCheck={false}
      autoCapitalize="off"
      autoCorrect="off"
      autoComplete="off"
      className={cn(
        "flex min-h-[60px] w-full rounded-md border border-input bg-control px-3 py-2",
        "font-mono text-xs leading-relaxed shadow-sm placeholder:text-muted-foreground",
        "focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
});
CodeTextarea.displayName = "CodeTextarea";

export { CodeTextarea };
