// errorCollector keeps a small ring buffer of recent console errors and
// uncaught exceptions. Imported once at app entry so the buffer fills up
// regardless of whether the user ever opens the bug-report dialog.

const recentErrors: string[] = [];
const MAX_ENTRIES = 10;

function push(line: string): void {
  recentErrors.push(line);
  if (recentErrors.length > MAX_ENTRIES) {
    recentErrors.shift();
  }
}

let installed = false;

export function installErrorCollector(): void {
  if (installed || typeof window === "undefined") return;
  installed = true;

  const originalConsoleError = console.error;
  console.error = (...args: unknown[]) => {
    try {
      push(args.map(stringify).join(" "));
    } catch {
      // Swallow — the ring buffer must never break logging.
    }
    originalConsoleError.apply(console, args as never);
  };

  window.addEventListener("error", (event) => {
    push(
      `${event.message} at ${event.filename ?? "?"}:${event.lineno ?? "?"}:${event.colno ?? "?"}`,
    );
  });

  window.addEventListener("unhandledrejection", (event) => {
    push(`Unhandled rejection: ${stringify(event.reason)}`);
  });
}

export function getRecentConsoleErrors(): string[] {
  return [...recentErrors];
}

function stringify(value: unknown): string {
  if (value === null || value === undefined) return String(value);
  if (typeof value === "string") return value;
  if (value instanceof Error) return value.stack || value.message;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
