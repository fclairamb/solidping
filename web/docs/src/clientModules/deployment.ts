/**
 * Flags `<html data-deployment="saas">` when these docs are served by the
 * SolidPing cloud, so the navbar's "Start free" shows there and nowhere else
 * (src/css/custom.css hides it otherwise). The same build ships inside every
 * self-hosted binary, so only the browser can tell:
 *
 *   - a docs host (docs.solidping.io) is the cloud's, and cannot answer API
 *     calls: handlerWithDocsHost redirects them into /docs;
 *   - anywhere else the serving instance says what it is, in the public
 *     GET /api/v1/config.
 *
 * Any failure leaves the flag off: a self-hosted page never shows the CTA.
 */
import ExecutionEnvironment from "@docusaurus/ExecutionEnvironment";
import siteConfig from "@generated/docusaurus.config";
import { docsHostsFrom, isKnownDocsHost } from "../lib/apiBaseUrl";
import { isSaasConfig } from "../lib/deployment";

function flagSaas(): void {
  document.documentElement.dataset.deployment = "saas";
}

if (ExecutionEnvironment.canUseDOM) {
  if (isKnownDocsHost(window.location.origin, docsHostsFrom(siteConfig.customFields))) {
    flagSaas();
  } else {
    fetch("/api/v1/config", { headers: { Accept: "application/json" } })
      .then((response) => (response.ok ? response.json() : null))
      .then((config) => {
        if (isSaasConfig(config)) flagSaas();
      })
      .catch(() => {
        // Not a SolidPing server (the docs dev server) or not JSON: the page
        // stays self-hosted.
      });
  }
}
