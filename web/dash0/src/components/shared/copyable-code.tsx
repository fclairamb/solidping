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
export function CopyableCode({
  code,
  "data-testid": testId,
}: {
  code: string;
  "data-testid"?: string;
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = () =>
    void copyToClipboard(code, () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    });

  return (
    <div className="relative" data-testid={testId}>
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
 * A copy-to-clipboard icon button for a value the caller already displays
 * elsewhere on the page. Set `inline` (default `true`) to also render the
 * value itself as a plain unboxed `<code>` next to the button — e.g. an ID
 * or URL in a form-field-style row. Pass `inline={false}` when the caller
 * renders its own styled value block alongside the button instead (e.g. a
 * `<pre>` error block) and only the bare button is needed. For a boxed,
 * always-visible block use `CopyableCode`; for a collapsible block use
 * `CollapsibleCode`.
 */
export function CopyableInline({
  value,
  label,
  inline = true,
  size = "sm",
}: {
  value: string;
  label?: string;
  inline?: boolean;
  size?: "sm" | "md";
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = () =>
    void copyToClipboard(value, () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    });

  const buttonSizeClass = size === "md" ? "h-7 w-7" : "h-6 w-6";
  const iconSizeClass = size === "md" ? "h-4 w-4" : "h-3.5 w-3.5";

  const button = (
    <button
      type="button"
      onClick={onCopy}
      aria-label={copied ? "Copied" : `Copy ${label ?? "value"}`}
      className={`inline-flex ${buttonSizeClass} shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground`}
    >
      {copied ? (
        <Check className={iconSizeClass} />
      ) : (
        <Copy className={iconSizeClass} />
      )}
    </button>
  );

  if (!inline) return button;

  return (
    <div className="flex items-start gap-2">
      <code className="min-w-0 break-all font-mono text-sm">{value}</code>
      {button}
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
  "data-testid": testId,
}: {
  label: string;
  value: string;
  defaultOpen?: boolean;
  "data-testid"?: string;
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = () =>
    void copyToClipboard(value, () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    });

  return (
    <details
      className="group rounded-md border"
      open={defaultOpen}
      data-testid={testId}
    >
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
