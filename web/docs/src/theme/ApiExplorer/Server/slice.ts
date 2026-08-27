/**
 * Swizzle of the OpenAPI theme's `server` redux slice.
 *
 * Written against **docusaurus-theme-openapi-docs@5.0.2** — re-check on upgrade.
 *
 * The theme preloads `server.options` from the operation's baked-in `servers:`
 * list (`@theme/ApiItem`), and its own `setServer` reducer only accepts a URL
 * that is *already* in that list:
 *
 *     setServer: (state, action) => {
 *       state.value = state.options.find((s) => s.url === JSON.parse(...).url);
 *     }
 *
 * So selecting the current instance's origin — which the spec never declares —
 * requires adding it to the options first. This wrapper leaves every existing
 * behaviour to the original reducer and adds a single action for that.
 *
 * `@theme/ApiItem/store` imports this module through the `@theme/` alias, so the
 * swizzled version is what the store is built from.
 */

import originalReducer from "@theme-original/ApiExplorer/Server/slice";

import type { ApiServer } from "@site/src/lib/apiBaseUrl";

export * from "@theme-original/ApiExplorer/Server/slice";

export interface ServerState {
  value?: ApiServer;
  options: ApiServer[];
}

export const SET_SERVER_OPTIONS = "server/setServerOptions";

/**
 * Replaces the list of servers offered by the Base URL control. The payload is
 * a JSON-encoded `ApiServer[]`, matching the theme's own string-payload
 * convention for this slice.
 */
export function setServerOptions(payload: string) {
  return { type: SET_SERVER_OPTIONS, payload } as const;
}

function reducer(
  state: ServerState | undefined,
  action: { type: string; payload?: unknown },
): ServerState {
  if (action?.type === SET_SERVER_OPTIONS) {
    const options = JSON.parse(String(action.payload)) as ApiServer[];
    const current = state?.value;
    const keepsCurrent =
      current && options.some((server) => server.url === current.url);

    return {
      ...state,
      options,
      value: keepsCurrent ? current : options[0],
    };
  }

  return originalReducer(state, action) as ServerState;
}

export default reducer;
