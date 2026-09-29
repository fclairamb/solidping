type Settings = Record<string, unknown> | null | undefined;

function str(settings: Settings, key: string): string {
  const value = settings?.[key];

  return typeof value === "string" ? value : "";
}

/**
 * Whether the Discord destination in the form is the one the server has
 * stored. The identities endpoints only answer for a stored bot destination
 * (guild + channel), so an unsaved pick or a changed channel / DM target is
 * "not saved yet", which is not an error.
 */
export function isDiscordDestinationSaved(
  current: Settings,
  saved: Settings,
): boolean {
  if (!str(saved, "guild_id") || !str(saved, "channel_id")) {
    return false;
  }

  return (
    str(current, "channel_id") === str(saved, "channel_id") &&
    str(current, "dm_user_id") === str(saved, "dm_user_id")
  );
}
