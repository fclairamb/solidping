/**
 * SolidPing embeddable status widget — v1 (spec 2026-08-08-08).
 *
 * Shipped as a single self-contained IIFE at `/embed/v1/widget.js` and pasted
 * onto third-party sites as:
 *
 *   <script async src="https://<host>/embed/v1/widget.js" data-page="org/slug"></script>
 *
 * FROZEN PUBLIC CONTRACT. Everything under `/embed/v1/` is pasted into pages
 * we do not control and can never be re-deployed by us. The attribute surface
 * below is deliberately minimal and MUST NOT change meaning: any behavior
 * change ships as `/embed/v2/widget.js` with its own route. New attributes MAY
 * be added within v1 as long as they are additive — omitting them must leave
 * existing pasted snippets behaving exactly as before (spec 2026-08-08-09).
 *
 * Supported data-attributes (all optional except `data-page`):
 *   data-page      "org/slug" — required, identifies the status page.
 *   data-mode      "inline" (default) | "floating"
 *   data-position  "bottom-right" (default) | "bottom-left"  (floating only)
 *   data-theme     "light" | "dark" | "auto" (default; follows
 *                  prefers-color-scheme)
 *   data-size      "sm" | "md" (default) | "lg" — scales font/padding/dot.
 *                  "md" renders pixel-identical to the pre-2026-08-08-09
 *                  widget.
 *   data-label-operational | -degraded | -down | -maintenance | -unknown
 *                  per-state label overrides; English defaults otherwise.
 *   data-force-status "operational" | "degraded" | "down" | "maintenance" |
 *                  "unknown" — skips polling entirely and renders that status
 *                  statically (no title, no link). An invalid or absent value
 *                  falls through to normal polling. Primarily a dash0 preview
 *                  affordance, but usable standalone (a static pill can
 *                  already be faked with plain HTML, so this adds no new
 *                  capability a hostile page didn't already have).
 *
 * SECURITY. This code executes on customer sites, so every value that reaches
 * the DOM is treated as hostile: labels come from host-page attributes, and
 * the page name / URL come over the network. There is intentionally NO use of
 * innerHTML / outerHTML / insertAdjacentHTML / document.write anywhere in this
 * file — text is written with textContent and the link target is validated to
 * an http(s) scheme before it becomes an href. A `javascript:` URL reaching
 * the anchor would be XSS on the customer's page.
 *
 * PRIVACY. The summary fetch is uncredentialed (`credentials: "omit"`); the
 * endpoint is public and the wildcard CORS policy would reject credentials
 * anyway.
 *
 * ENTITLEMENT HOOK (not implemented, spec 2026-08-08-08): the v1 pill carries
 * no SolidPing attribution. If attribution is ever added (e.g. a "powered by"
 * line in a hover/expanded state), removing it is the white-label paid
 * entitlement flagged in wiki/roadmap.md — it would attach here, driven by a
 * field on the summary response, never by a host-page attribute.
 */

type PageStatus =
  | "operational"
  | "degraded"
  | "down"
  | "maintenance"
  | "unknown";

interface SummaryPage {
  name?: unknown;
  slug?: unknown;
  url?: unknown;
}

interface SummaryResponse {
  status?: unknown;
  page?: SummaryPage;
}

const STATUSES: PageStatus[] = [
  "operational",
  "degraded",
  "down",
  "maintenance",
  "unknown",
];

const DEFAULT_LABELS: Record<PageStatus, string> = {
  operational: "All systems operational",
  degraded: "Degraded performance",
  down: "Major outage",
  maintenance: "Under maintenance",
  unknown: "Status unknown",
};

const POLL_INTERVAL_MS = 60_000;

/** Static stylesheet — no interpolation of any untrusted value, ever. */
const STYLES = `
:host { all: initial; display: inline-block; }
.sp-root {
  --sp-bg: #ffffff;
  --sp-fg: #1f2937;
  --sp-border: #e5e7eb;
  --sp-shadow: 0 1px 2px rgba(0,0,0,.08);
  display: inline-block;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto,
    Helvetica, Arial, sans-serif;
}
.sp-root.sp-dark {
  --sp-bg: #111827;
  --sp-fg: #e5e7eb;
  --sp-border: #374151;
  --sp-shadow: 0 1px 2px rgba(0,0,0,.4);
}
@media (prefers-color-scheme: dark) {
  .sp-root.sp-auto {
    --sp-bg: #111827;
    --sp-fg: #e5e7eb;
    --sp-border: #374151;
    --sp-shadow: 0 1px 2px rgba(0,0,0,.4);
  }
}
.sp-root.sp-floating {
  position: fixed;
  bottom: 16px;
  z-index: 2147483000;
}
.sp-root.sp-floating.sp-bottom-right { right: 16px; }
.sp-root.sp-floating.sp-bottom-left { left: 16px; }
.sp-pill {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  box-sizing: border-box;
  max-width: 100%;
  padding: 6px 12px;
  border: 1px solid var(--sp-border);
  border-radius: 9999px;
  background: var(--sp-bg);
  color: var(--sp-fg);
  box-shadow: var(--sp-shadow);
  font-size: 13px;
  line-height: 1.2;
  font-weight: 500;
  text-decoration: none;
  cursor: default;
}
a.sp-pill { cursor: pointer; }
a.sp-pill:hover { border-color: var(--sp-dot-color); }
.sp-dot {
  flex: none;
  width: 9px;
  height: 9px;
  border-radius: 9999px;
  background: var(--sp-dot-color);
  box-shadow: 0 0 0 3px var(--sp-dot-halo);
}
.sp-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.sp-root.sp-size-sm .sp-pill { padding: 4px 9px; gap: 6px; font-size: 11px; }
.sp-root.sp-size-sm .sp-dot { width: 7px; height: 7px; }
.sp-root.sp-size-lg .sp-pill { padding: 8px 16px; gap: 10px; font-size: 15px; }
.sp-root.sp-size-lg .sp-dot { width: 11px; height: 11px; }
.sp-operational { --sp-dot-color: #16a34a; --sp-dot-halo: rgba(22,163,74,.18); }
.sp-degraded    { --sp-dot-color: #d97706; --sp-dot-halo: rgba(217,119,6,.18); }
.sp-down        { --sp-dot-color: #dc2626; --sp-dot-halo: rgba(220,38,38,.18); }
.sp-maintenance { --sp-dot-color: #2563eb; --sp-dot-halo: rgba(37,99,235,.18); }
.sp-unknown     { --sp-dot-color: #9ca3af; --sp-dot-halo: rgba(156,163,175,.18); }
`;

/**
 * Locates the <script> element this code is running from. `document
 * .currentScript` is set even for `async` classic scripts during execution;
 * the src scan is the belt-and-braces fallback for exotic loaders.
 */
function findScript(): HTMLScriptElement | null {
  const current = document.currentScript;
  if (current && current instanceof HTMLScriptElement) {
    return current;
  }

  const scripts = document.getElementsByTagName("script");
  for (let i = scripts.length - 1; i >= 0; i--) {
    const candidate = scripts[i];
    if (
      candidate.src &&
      candidate.src.indexOf("/embed/v1/widget.js") !== -1 &&
      candidate.getAttribute("data-page")
    ) {
      return candidate;
    }
  }

  return null;
}

/**
 * Builds the summary endpoint URL. `data-page` is attacker-controllable by
 * whoever pastes the snippet, so each segment is URL-encoded rather than
 * concatenated into the path — a slug of `../../admin` must not escape the
 * status-page namespace.
 */
function summaryURL(origin: string, dataPage: string): string | null {
  const parts = dataPage.split("/");
  if (parts.length !== 2) {
    return null;
  }

  const org = parts[0].trim();
  const slug = parts[1].trim();
  if (!org || !slug) {
    return null;
  }

  return (
    origin +
    "/api/v1/status-pages/" +
    encodeURIComponent(org) +
    "/" +
    encodeURIComponent(slug) +
    "/summary"
  );
}

/**
 * Returns `raw` only when it is an absolute http(s) URL, otherwise null. This
 * is the gate that keeps a hostile `page.url` (`javascript:alert(1)`,
 * `data:text/html,...`) from ever becoming an href on the customer's page.
 */
function safeHref(raw: unknown): string | null {
  if (typeof raw !== "string" || !raw) {
    return null;
  }

  try {
    const parsed = new URL(raw, window.location.href);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return null;
    }

    return parsed.href;
  } catch {
    return null;
  }
}

function normalizeStatus(raw: unknown): PageStatus {
  if (typeof raw === "string") {
    for (const known of STATUSES) {
      if (raw === known) {
        return known;
      }
    }
  }

  return "unknown";
}

function readLabels(script: HTMLScriptElement): Record<PageStatus, string> {
  const labels = { ...DEFAULT_LABELS };
  for (const status of STATUSES) {
    const override = script.getAttribute("data-label-" + status);
    if (override) {
      labels[status] = override;
    }
  }

  return labels;
}

function themeClass(script: HTMLScriptElement): string {
  const theme = (script.getAttribute("data-theme") || "auto").toLowerCase();
  if (theme === "light") {
    return "sp-light";
  }
  if (theme === "dark") {
    return "sp-dark";
  }

  return "sp-auto";
}

/**
 * `"md"` (default, and any unrecognized value) maps to the empty string so no
 * extra class is added — the base `.sp-pill`/`.sp-dot` rules apply exactly as
 * they did before this attribute existed, which is what keeps `md` pixel-
 * identical to the pre-2026-08-08-09 widget.
 */
function sizeClass(script: HTMLScriptElement): string {
  const size = (script.getAttribute("data-size") || "md").toLowerCase();
  if (size === "sm") {
    return "sp-size-sm";
  }
  if (size === "lg") {
    return "sp-size-lg";
  }

  return "";
}

/**
 * Reads `data-force-status`, returning a valid `PageStatus` only for an exact
 * match against the known set — anything else (typo, empty, absent) yields
 * `null` so the caller falls through to normal polling.
 */
function readForceStatus(script: HTMLScriptElement): PageStatus | null {
  const raw = script.getAttribute("data-force-status");
  if (typeof raw === "string") {
    for (const known of STATUSES) {
      if (raw === known) {
        return known;
      }
    }
  }

  return null;
}

interface Widget {
  render(status: PageStatus, label: string, title: string, href: string | null): void;
}

/**
 * Creates the host element + shadow root lazily, on the first successful
 * fetch. Nothing is inserted into the customer's page before that, which is
 * what makes "404 or network failure renders nothing" true by construction
 * rather than by cleanup.
 */
function createWidget(script: HTMLScriptElement): Widget {
  const floating =
    (script.getAttribute("data-mode") || "inline").toLowerCase() === "floating";
  const position =
    (script.getAttribute("data-position") || "bottom-right").toLowerCase() ===
    "bottom-left"
      ? "sp-bottom-left"
      : "sp-bottom-right";
  const theme = themeClass(script);
  const size = sizeClass(script);

  let host: HTMLElement | null = null;
  let root: HTMLElement | null = null;
  let pill: HTMLElement | null = null;
  let dot: HTMLElement | null = null;
  let labelEl: HTMLElement | null = null;

  const mount = (linked: boolean) => {
    if (!host) {
      host = document.createElement("div");
      host.setAttribute("data-solidping-widget", "");
      if (floating) {
        document.body.appendChild(host);
      } else {
        const parent = script.parentNode;
        if (parent) {
          parent.insertBefore(host, script.nextSibling);
        } else {
          document.body.appendChild(host);
        }
      }

      const shadow = host.attachShadow({ mode: "open" });
      const style = document.createElement("style");
      style.textContent = STYLES;
      shadow.appendChild(style);

      root = document.createElement("div");
      root.className =
        "sp-root " +
        theme +
        (size ? " " + size : "") +
        (floating ? " sp-floating " + position : "");
      shadow.appendChild(root);
    }

    // The pill is an <a> only when there is a validated http(s) target; with
    // no safe target it degrades to a non-interactive <span> rather than an
    // anchor with a suspicious href.
    const wanted = linked ? "a" : "span";
    if (!pill || pill.tagName.toLowerCase() !== wanted) {
      const next = document.createElement(wanted);
      next.className = "sp-pill";
      next.setAttribute("data-testid", "solidping-widget-pill");
      if (linked) {
        next.setAttribute("target", "_blank");
        next.setAttribute("rel", "noopener noreferrer");
      }

      dot = document.createElement("span");
      dot.className = "sp-dot";
      dot.setAttribute("part", "dot");
      next.appendChild(dot);

      labelEl = document.createElement("span");
      labelEl.className = "sp-label";
      labelEl.setAttribute("data-testid", "solidping-widget-label");
      next.appendChild(labelEl);

      if (pill && root) {
        root.replaceChild(next, pill);
      } else if (root) {
        root.appendChild(next);
      }

      pill = next;
    }
  };

  return {
    render(status, label, title, href) {
      mount(href !== null);
      if (!pill || !labelEl || !root) {
        return;
      }

      root.setAttribute("data-status", status);
      pill.className = "sp-pill sp-" + status;
      // textContent, never innerHTML — `label` and `title` are attacker
      // controllable (host-page attribute / status page name).
      labelEl.textContent = label;
      pill.setAttribute("title", title);
      if (href !== null) {
        pill.setAttribute("href", href);
      } else {
        pill.removeAttribute("href");
      }
    },
  };
}

function boot(script: HTMLScriptElement): void {
  const dataPage = script.getAttribute("data-page");
  if (!dataPage) {
    return;
  }

  const labels = readLabels(script);
  const widget = createWidget(script);

  // `data-force-status`: render statically and stop — deliberately checked
  // before any URL/origin resolution so this path makes zero network calls,
  // which is what lets the dash0 preview render before a status page is even
  // published.
  const forceStatus = readForceStatus(script);
  if (forceStatus) {
    widget.render(forceStatus, labels[forceStatus], "", null);
    return;
  }

  let origin: string;
  try {
    origin = new URL(script.src, window.location.href).origin;
  } catch {
    return;
  }

  const url = summaryURL(origin, dataPage);
  if (!url) {
    return;
  }

  const poll = () => {
    // Uncredentialed by design: the endpoint is public and wildcard CORS
    // forbids credentialed requests anyway.
    window
      .fetch(url, {
        credentials: "omit",
        mode: "cors",
        headers: { Accept: "application/json" },
      })
      .then((response) => (response.ok ? response.json() : null))
      .then((body: SummaryResponse | null) => {
        if (!body) {
          // 404 / 5xx: render nothing (never an error state on the
          // customer's site). Anything already rendered from an earlier
          // successful poll is left alone rather than flickering away on a
          // transient blip.
          return;
        }

        const status = normalizeStatus(body.status);
        const page = body.page || {};
        const title = typeof page.name === "string" ? page.name : "";
        widget.render(status, labels[status], title, safeHref(page.url));
      })
      .catch(() => {
        /* Network failure: same graceful degradation as above. */
      });
  };

  poll();
  window.setInterval(poll, POLL_INTERVAL_MS);
}

// The script element is resolved SYNCHRONOUSLY: document.currentScript is only
// valid during this initial execution, so it must not be read from a deferred
// callback. Only the DOM mounting waits for <body>.
const ownScript = findScript();
if (ownScript) {
  if (document.body) {
    boot(ownScript);
  } else {
    document.addEventListener("DOMContentLoaded", () => boot(ownScript));
  }
}
