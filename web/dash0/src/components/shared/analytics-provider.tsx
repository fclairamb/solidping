import { useEffect } from "react";
import { fetchPublicConfig, initAnalytics } from "@/lib/analytics";

/**
 * Boots product analytics.
 *
 * Mounted once from the root route. It reads `GET /api/v1/config` and, ONLY if
 * that reports PostHog as enabled, dynamically imports and initializes
 * `posthog-js`. On a deployment with no PostHog credentials this component
 * performs exactly one same-origin request and nothing else: no analytics chunk
 * is downloaded and no third-party host is contacted.
 *
 * Renders nothing.
 */
export function AnalyticsProvider() {
  useEffect(() => {
    let cancelled = false;

    void (async () => {
      const config = await fetchPublicConfig();
      if (cancelled) return;
      await initAnalytics(config);
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  return null;
}
