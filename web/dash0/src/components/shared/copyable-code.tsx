import { useState } from "react";
import { Check, ChevronRight, Copy } from "lucide-react";

/** Shared "copied" reset delay for the copy-to-clipboard affordance below. */
const COPIED_RESET_MS = 1500;

async function copyToClipboard(value: string, onCopied: () => void) {
  try {
    await navigator.clipboard.writeText(value);
    onCopied();
  } catch {
    // Clipboard API requires a secure context; ignore failures silently —
    // the value stays visible on the page so it can still be copied by hand.
  }
}

/**
 * A single-line (or short) monospace value with an inline copy-to-clipboard
 * button. Use for short values — URLs, one-liner commands — that should
 * always be visible without an extra click. For longer/optional payloads
 * (raw JSON, request bodies) that should default to collapsed, use
 * `CollapsibleCode` instead.
 */
export function CopyableCode({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = () =>
    void copyToClipboard(code, () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    });

  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-md border bg-muted/40 px-3 py-2 pr-12 text-xs leading-relaxed">
        <code>{code}</code>
      </pre>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? "Copied" : "Copy to clipboard"}
        className="absolute right-1.5 top-1.5 inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
      >
        {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
      </button>
    </div>
  );
}

/**
 * A collapsible monospace block with a copy-to-clipboard affordance —
 * native `<details>`/`<summary>`, keyboard-accessible, no JS-driven layout.
 * Use for long or optional payloads (raw JSON, request/response bodies)
 * that should default to collapsed. For short always-visible values, use
 * `CopyableCode` instead.
 */
export function CollapsibleCode({
  label,
  value,
  defaultOpen = false,
}: {
  label: string;
  value: string;
  defaultOpen?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = () =>
    void copyToClipboard(value, () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    });

  return (
    <details className="group rounded-md border" open={defaultOpen}>
      <summary className="flex cursor-pointer list-none items-center justify-between gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent">
        <span className="flex min-w-0 items-center gap-2">
          <ChevronRight className="h-4 w-4 shrink-0 transition-transform group-open:rotate-90" />
          <span className="truncate font-medium">{label}</span>
        </span>
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            onCopy();
          }}
          aria-label={copied ? "Copied" : `Copy ${label}`}
          className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-background hover:text-foreground"
        >
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
        </button>
      </summary>
      <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words border-t bg-muted p-3 font-mono text-xs">
        {value}
      </pre>
    </details>
  );
}
