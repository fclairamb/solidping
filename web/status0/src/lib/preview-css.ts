import { useEffect, useState } from "react";

/**
 * Message the dash0 appearance editor posts into the preview iframe.
 *
 * Kept intentionally boring: a literal discriminator plus a string payload, so
 * the receiving side can validate it exhaustively without a schema library.
 */
export const PREVIEW_CSS_MESSAGE_TYPE = "sp:preview-css";

export interface PreviewCssMessage {
  type: typeof PREVIEW_CSS_MESSAGE_TYPE;
  css: string;
}

/**
 * Returns true when the current URL asks for preview mode (`?preview=1`).
 *
 * Preview mode is the ONLY state in which this page listens for postMessage.
 * A normal public visit installs no listener at all, so there is no message
 * surface to attack on the real status page.
 */
export function isPreviewMode(): boolean {
  if (typeof window === "undefined") return false;
  return new URLSearchParams(window.location.search).get("preview") === "1";
}

/**
 * Narrows an untrusted `MessageEvent.data` to a PreviewCssMessage.
 *
 * Anything that is not an object with exactly the expected discriminator and a
 * string `css` is rejected — including the framework noise other tools post
 * into iframes (React DevTools, Vite HMR, webpack, extensions).
 */
function isPreviewCssMessage(data: unknown): data is PreviewCssMessage {
  if (typeof data !== "object" || data === null) return false;
  const candidate = data as Record<string, unknown>;
  return (
    candidate.type === PREVIEW_CSS_MESSAGE_TYPE &&
    typeof candidate.css === "string"
  );
}

/**
 * Resolves the stylesheet the page should render.
 *
 * Outside preview mode this is simply the value fetched from the API. Inside
 * preview mode (`?preview=1`) the page also accepts live drafts from its
 * embedder, but only when BOTH hold:
 *
 *   1. `event.origin === window.location.origin` — dash0 and status0 are served
 *      from the same origin, so a cross-origin frame can never drive the
 *      preview; and
 *   2. the payload matches the `{type: 'sp:preview-css', css: string}` shape.
 *
 * A draft (including the empty string, which is how the editor shows "no
 * custom CSS") wins over the fetched value until the next message.
 */
export function usePreviewCss(fetched: string | undefined): string | undefined {
  const [draft, setDraft] = useState<string | undefined>(undefined);

  useEffect(() => {
    if (!isPreviewMode()) return;

    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return;
      if (!isPreviewCssMessage(event.data)) return;
      setDraft(event.data.css);
    };

    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, []);

  return draft ?? fetched;
}
