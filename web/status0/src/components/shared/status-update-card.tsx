import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Badge } from "@/components/ui/badge";
import type { BadgeProps } from "@/components/ui/badge";
import type { StatusUpdatePublicResponse } from "@/api/hooks";

// ALLOWED_SCHEMES defines URL schemes that are safe to render as links.
const ALLOWED_SCHEMES = ["http", "https", "mailto"];

function SafeMarkdown({ content }: { content: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      skipHtml
      components={{
        a: ({ href, children, ...props }) => {
          const scheme = href?.split(":")[0] ?? "";
          if (!ALLOWED_SCHEMES.includes(scheme)) return <>{children}</>;
          return (
            <a href={href} rel="noopener noreferrer" target="_blank" {...props}>
              {children}
            </a>
          );
        },
        // Cap heading depth so update content doesn't disrupt page hierarchy
        h1: "h3",
        h2: "h3",
        h3: "h3",
      }}
    >
      {content}
    </ReactMarkdown>
  );
}

function kindBadgeVariant(kind: string): BadgeProps["variant"] {
  switch (kind) {
    case "investigating":
    case "identified":
      return "warning";
    case "resolved":
      return "success";
    case "monitoring":
      return "default";
    case "maintenance":
    case "info":
    default:
      return "secondary";
  }
}

function kindLabel(kind: string): string {
  if (!kind) return "Update";
  return kind.charAt(0).toUpperCase() + kind.slice(1);
}

function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60_000);

  if (diffMins < 1) return "just now";
  if (diffMins < 60)
    return `${diffMins} minute${diffMins === 1 ? "" : "s"} ago`;

  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24)
    return `${diffHours} hour${diffHours === 1 ? "" : "s"} ago`;

  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays} day${diffDays === 1 ? "" : "s"} ago`;
}

/**
 * "card" (default): full chrome — rounded border, surface background, own
 * padding. "plain": no chrome/padding, for embedding inside a container that
 * already provides them (e.g. StatusUpdateThreadList's own `p-4` row nested
 * in an already-bordered incident card or "Incident thread" section).
 * Everything else — id/scroll anchor, header row, badges, hardened Markdown
 * body, "Read more" link, and all data-testid hooks — is unchanged.
 */
export type StatusUpdateCardVariant = "card" | "plain";

interface StatusUpdateCardProps {
  update: StatusUpdatePublicResponse;
  variant?: StatusUpdateCardVariant;
}

export function StatusUpdateCard({
  update,
  variant = "card",
}: StatusUpdateCardProps) {
  return (
    <div
      id={`update-${update.uid}`}
      className={
        variant === "plain"
          ? "scroll-mt-24 space-y-2"
          : "scroll-mt-24 rounded-lg border border-border bg-card p-4 space-y-2"
      }
    >
      {/* Header row: title (left) + kind badge + timestamp (right) */}
      <div className="flex items-start justify-between gap-2 flex-wrap">
        <h3 className="text-sm font-semibold text-foreground min-w-0 break-words">
          {update.title}
        </h3>
        {/* translate="no" on both: the kind label flips
            investigating → identified → resolved as the incident progresses,
            and the relative timestamp is recomputed on every render, so React
            rewrites these text nodes while their elements are reused. Neither
            is operator-authored. See NO_TRANSLATE in status-page-view.tsx. */}
        <div className="flex items-center gap-2 shrink-0">
          <Badge
            variant={kindBadgeVariant(update.kind)}
            aria-label={`Update kind: ${kindLabel(update.kind)}`}
            data-testid="status-update-kind"
            translate="no"
          >
            {kindLabel(update.kind)}
          </Badge>
          <time
            dateTime={update.publishedAt}
            className="text-xs text-muted-foreground"
            data-testid="status-update-time"
            translate="no"
          >
            {formatRelativeTime(update.publishedAt)}
          </time>
        </div>
      </div>

      {/* Body markdown */}
      {update.bodyMarkdown && (
        <div className="prose prose-sm max-w-none text-sm text-foreground [&>*:last-child]:mb-0">
          <SafeMarkdown content={update.bodyMarkdown} />
        </div>
      )}

      {/* Optional "Read more" link */}
      {update.linkUrl && (
        <a
          href={update.linkUrl}
          rel="noopener noreferrer"
          target="_blank"
          className="text-xs text-primary hover:underline"
        >
          Read more →
        </a>
      )}
    </div>
  );
}
