// The container image a private-location agent snippet runs (spec
// 2026-11-22-04).
//
// A tagged server served over HTTPS is its own registry: `<host>/solidping:current`
// always resolves to the image of the server's version, so an agent that
// restarts (when the server asks it to) comes back on that version. Docker and
// containerd only talk to a registry over HTTPS (localhost aside), so an
// http:// server pins the exact upstream tag instead. A development build has
// no published image of its own: `:latest`.

export const AGENT_UPSTREAM_IMAGE = "ghcr.io/fclairamb/solidping";

export interface AgentImage {
  /** The image reference the snippets use. */
  image: string;
  /** True when the image follows the server's version (the server registry). */
  followsServer: boolean;
  /** The exact upstream tag of the server's version, when it has one. */
  pinnedImage: string | null;
}

// A release tag as the CI publishes it (1.2.3, 1.2.3-rc.1). A `git describe`
// build (1.2.3-4-gdeadbee) or "dev" has no image.
const RELEASE_VERSION = /^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$/;

export function releaseVersion(version: string | undefined): string | null {
  const v = (version ?? "").trim().replace(/^v/, "");
  return RELEASE_VERSION.test(v) ? v : null;
}

export function agentImage(version: string | undefined, origin: string): AgentImage {
  const release = releaseVersion(version);
  if (!release) {
    return { image: `${AGENT_UPSTREAM_IMAGE}:latest`, followsServer: false, pinnedImage: null };
  }

  const pinnedImage = `${AGENT_UPSTREAM_IMAGE}:${release}`;

  let url: URL | null = null;
  try {
    url = new URL(origin);
  } catch {
    url = null;
  }

  if (url && url.protocol === "https:") {
    return { image: `${url.host}/solidping:current`, followsServer: true, pinnedImage };
  }

  return { image: pinnedImage, followsServer: false, pinnedImage };
}
