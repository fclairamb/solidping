/**
 * Swizzle of the OpenAPI theme's Base URL control.
 *
 * Written against **docusaurus-theme-openapi-docs@5.0.2** — re-check on upgrade.
 * The upstream component (`@theme-original/ApiExplorer/Server`) is rendered
 * as-is; all this wrapper does is decide which server it should offer first.
 *
 * The reference pages are generated at build time, so every page ships the
 * spec's `servers:` list verbatim and a reader on a self-hosted instance is told
 * to call `https://solidping.io`. Here we prefer the origin actually serving the
 * page — except on the documentation host, which redirects API paths into the
 * docs site and where the cloud entry is the correct answer. The decision itself
 * lives in `@site/src/lib/apiBaseUrl`, which is pure and unit-tested.
 *
 * The rendered code samples read the same `state.server.value`, so they follow
 * the control.
 */

import React, { useEffect } from "react";

import useDocusaurusContext from "@docusaurus/useDocusaurusContext";
import OriginalServer from "@theme-original/ApiExplorer/Server";
import { useTypedDispatch, useTypedSelector } from "@theme/ApiItem/hooks";

// Relative, like the theme's own component: this is the swizzled slice sitting
// next to us, which `@theme/ApiItem/store` also picks up through the alias.
import { setServer, setServerOptions } from "./slice";

import {
  docsHostsFrom,
  resolveApiServers,
  sameServerUrls,
  shouldApplyDefaultSelection,
  type ApiServer,
} from "@site/src/lib/apiBaseUrl";

interface ServerProps {
  labelId?: string;
}

/**
 * Where we remember the URL *we* selected on our own, so a reader's deliberate
 * pick survives navigation to another reference page (the theme persists its
 * own automatic default identically to a click, so the stored selection alone
 * cannot tell the two apart — see shouldApplyDefaultSelection).
 *
 * sessionStorage matches the theme's default `authPersistence`; if a site sets
 * that option to localStorage the marker simply goes missing each new session
 * and the host default is applied again, which is the previous behaviour.
 */
const AUTO_SELECTED_KEY = "solidping.apiBaseUrl.autoSelected";

function readAutoSelected(): string | null {
  try {
    return window.sessionStorage.getItem(AUTO_SELECTED_KEY);
  } catch {
    return null; // Storage disabled (private mode, blocked cookies).
  }
}

function rememberAutoSelected(url: string): void {
  try {
    window.sessionStorage.setItem(AUTO_SELECTED_KEY, url);
  } catch {
    // Storage disabled; we just re-apply the default on the next page.
  }
}

export default function Server(props: ServerProps): React.JSX.Element | null {
  const { siteConfig } = useDocusaurusContext();
  const dispatch = useTypedDispatch();
  const options = useTypedSelector(
    (state: { server: { options?: ApiServer[] } }) => state.server.options,
  );
  const value = useTypedSelector(
    (state: { server: { value?: ApiServer } }) => state.server.value,
  );

  // Stable across renders (a build-time constant), so it is safe as a dep.
  const docsHosts = docsHostsFrom(siteConfig.customFields).join(",");

  useEffect(() => {
    const current = options ?? [];

    if (typeof window === "undefined" || current.length === 0) {
      return;
    }

    const resolved = resolveApiServers(
      window.location.origin,
      docsHosts === "" ? [] : docsHosts.split(","),
      current,
    );

    // resolveApiServers is idempotent, so this settles after one pass.
    if (sameServerUrls(resolved, current)) {
      return;
    }

    // Offer the resolved list. A selection the reader made themselves survives
    // this: the swizzled reducer keeps any value still present in the new list.
    dispatch(setServerOptions(JSON.stringify(resolved)));

    const preferred = resolved[0];

    if (!preferred || !shouldApplyDefaultSelection(value?.url, readAutoSelected())) {
      return;
    }

    // Go through the theme's own action so the choice is persisted the way a
    // manual selection would be.
    dispatch(setServer(JSON.stringify(preferred)));
    rememberAutoSelected(preferred.url);
  }, [dispatch, docsHosts, options, value]);

  return <OriginalServer {...props} />;
}
