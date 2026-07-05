// Small, hand-rolled user-agent parser for the sessions page. This is
// deliberately not a general-purpose UA parser (no bundled dependency,
// no exhaustive device/browser database) — it covers the common desktop and
// mobile browsers well enough to render a friendly "Chrome 128 on macOS" +
// device icon, and falls back gracefully on anything else. Distinct from
// `browser-detection.ts`'s deriveDeviceLabel, which only ever inspects the
// *current* browser's navigator.userAgent for notification-subscription
// labeling; this parses an arbitrary stored UA string (another device's).

export type DeviceType = "mobile" | "tablet" | "desktop";

export interface ParsedUserAgent {
  browser?: string;
  browserVersion?: string;
  os?: string;
  device: DeviceType;
}

function detectDevice(ua: string): DeviceType {
  if (/iPad|Tablet(?!.*Mobile)/.test(ua)) return "tablet";
  if (/Android/.test(ua) && !/Mobile/.test(ua)) return "tablet";
  if (/Mobile|iPhone|iPod|Android/.test(ua)) return "mobile";
  return "desktop";
}

function detectBrowser(ua: string): { browser?: string; browserVersion?: string } {
  // Order matters: Edge/Opera/Samsung Browser also match the Chrome/Safari
  // regexes, so check the more specific tokens first.
  const patterns: Array<[string, RegExp]> = [
    ["Edge", /Edg\/([\d.]+)/],
    ["Opera", /(?:OPR|Opera)\/([\d.]+)/],
    ["Samsung Internet", /SamsungBrowser\/([\d.]+)/],
    ["Firefox", /Firefox\/([\d.]+)/],
    ["Chrome", /Chrome\/([\d.]+)/],
    ["Safari", /Version\/([\d.]+).*Safari\//],
  ];

  for (const [name, re] of patterns) {
    const match = ua.match(re);
    if (match) {
      return { browser: name, browserVersion: match[1].split(".")[0] };
    }
  }

  return {};
}

function detectOS(ua: string): string | undefined {
  if (/Windows/.test(ua)) return "Windows";
  if (/iPhone|iPad|iPod/.test(ua)) return "iOS";
  if (/Macintosh|Mac OS X/.test(ua)) return "macOS";
  if (/Android/.test(ua)) return "Android";
  if (/Linux/.test(ua)) return "Linux";
  return undefined;
}

/** Parses a raw `User-Agent` header value into browser/OS/device fields for
 * display. Returns best-effort undefineds for anything it can't recognize —
 * callers should fall back to showing the raw UA string in that case. */
export function parseUserAgent(ua: string | undefined | null): ParsedUserAgent {
  if (!ua) {
    return { device: "desktop" };
  }

  const { browser, browserVersion } = detectBrowser(ua);

  return {
    browser,
    browserVersion,
    os: detectOS(ua),
    device: detectDevice(ua),
  };
}
