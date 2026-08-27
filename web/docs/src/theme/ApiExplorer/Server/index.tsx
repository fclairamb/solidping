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
import { setServer, setServerOptions } from "@theme/ApiExplorer/Server/slice";
import { useTypedDispatch, useTypedSelector } from "@theme/ApiItem/hooks";

import {
  docsHostsFrom,
  resolveApiServers,
  sameServerUrls,
  type ApiServer,
} from "@site/src/lib/apiBaseUrl";

interface ServerProps {
  labelId?: string;
}

export default function Server(props: ServerProps): React.JSX.Element | null {
  const { siteConfig } = useDocusaurusContext();
  const dispatch = useTypedDispatch();
  const options = useTypedSelector(
    (state: { server: { options?: ApiServer[] } }) => state.server.options,
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

    dispatch(setServerOptions(JSON.stringify(resolved)));

    // Go through the theme's own action so the choice is persisted the way a
    // manual selection would be.
    if (resolved[0]) {
      dispatch(setServer(JSON.stringify(resolved[0])));
    }
  }, [dispatch, docsHosts, options]);

  return <OriginalServer {...props} />;
}
