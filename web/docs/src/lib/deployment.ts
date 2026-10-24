/**
 * Whether a `GET /api/v1/config` document comes from the SolidPing cloud. The
 * server sets `deploymentMode` to "saas" or "self-hosted"
 * (server/internal/handlers/publicconfig); anything else, including no
 * document at all, counts as self-hosted.
 */
export function isSaasConfig(config: unknown): boolean {
  return (
    typeof config === "object" &&
    config !== null &&
    (config as { deploymentMode?: unknown }).deploymentMode === "saas"
  );
}
