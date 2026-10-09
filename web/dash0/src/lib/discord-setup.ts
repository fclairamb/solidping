/**
 * True when the server still has its default base URL but the dashboard is
 * opened through another host: Discord would redirect to localhost.
 */
export function showLocalhostWarning(
  baseUrlIsDefault: boolean,
  currentHostname: string,
): boolean {
  if (!baseUrlIsDefault) return false;
  return !["localhost", "127.0.0.1", "[::1]", "::1"].includes(currentHostname);
}
